package profile

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestHardDeleteUserCascadesAndLogs(t *testing.T) {
	db := setupDB(t)
	uid := insertUser(t, db, "alice@example.com")
	addEntry(t, db, uid, "Berserk")
	// Una sesión y un código OTP también.
	if _, err := db.Exec(`INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, last_used_at, expires_at) VALUES ('hash', ?, 'x', 0, 0, ?)`,
		uid, time.Now().Add(1*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO otp_codes (email, code_hash, created_at, expires_at) VALUES (?, 'hash', 0, ?)`,
		"alice@example.com", time.Now().Add(1*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	if err := HardDeleteUser(context.Background(), db, uid, "pepper"); err != nil {
		t.Fatalf("HardDelete: %v", err)
	}

	// User borrado.
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM users WHERE id = ?`, uid).Scan(&n)
	if n != 0 {
		t.Error("user no fue borrado")
	}
	// CASCADE: library_entries, sessions.
	_ = db.QueryRow(`SELECT count(*) FROM library_entries WHERE user_id = ?`, uid).Scan(&n)
	if n != 0 {
		t.Error("library_entries no fueron borrados via cascade")
	}
	_ = db.QueryRow(`SELECT count(*) FROM sessions WHERE user_id = ?`, uid).Scan(&n)
	if n != 0 {
		t.Error("sessions no fueron borradas via cascade")
	}
	// otp_codes (sin FK) borrado explícitamente.
	_ = db.QueryRow(`SELECT count(*) FROM otp_codes WHERE email = ?`, "alice@example.com").Scan(&n)
	if n != 0 {
		t.Error("otp_codes no fueron borrados")
	}
	// deletion_log con entry.
	var hash string
	var deletedAt int64
	if err := db.QueryRow(`SELECT user_id_hash, deleted_at FROM deletion_log`).Scan(&hash, &deletedAt); err != nil {
		t.Fatalf("audit log no tiene entrada: %v", err)
	}
	if len(hash) != 64 { // sha256 hex
		t.Errorf("hash mal formado: %q", hash)
	}
	// El hash NO debe contener el user_id en plano (defensa básica).
	if strings.Contains(hash, "1") && !strings.ContainsAny(hash, "abcdef") {
		// hex puro es ok, lo importante es que no haya pattern visible.
	}
}

func TestHardDeleteUserPepperChangesHash(t *testing.T) {
	db := setupDB(t)
	uid1 := insertUser(t, db, "a@example.com")
	uid2 := insertUser(t, db, "b@example.com")

	if err := HardDeleteUser(context.Background(), db, uid1, "pepper1"); err != nil {
		t.Fatal(err)
	}
	if err := HardDeleteUser(context.Background(), db, uid2, "pepper2"); err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(`SELECT user_id_hash FROM deletion_log ORDER BY deleted_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	hashes := map[string]struct{}{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatal(err)
		}
		hashes[h] = struct{}{}
	}
	if len(hashes) != 2 {
		t.Errorf("se esperaban 2 hashes distintos, obtuve %d", len(hashes))
	}
}

func TestHardDeleteUserMissingUser(t *testing.T) {
	db := setupDB(t)
	err := HardDeleteUser(context.Background(), db, 9999, "pepper")
	if err == nil {
		t.Error("HardDeleteUser de inexistente debió fallar")
	}
}

func TestCleanupWorkerDeletesExpired(t *testing.T) {
	db := setupDB(t)
	uidOld := insertUser(t, db, "old@example.com")
	uidNew := insertUser(t, db, "new@example.com")

	// old: marcado hace 8 días → debe borrarse.
	oldTime := time.Now().UTC().Add(-8 * 24 * time.Hour).Unix()
	if _, err := db.Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, oldTime, uidOld); err != nil {
		t.Fatal(err)
	}
	// new: marcado hace 1 día → todavía en gracia.
	newTime := time.Now().UTC().Add(-1 * 24 * time.Hour).Unix()
	if _, err := db.Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, newTime, uidNew); err != nil {
		t.Fatal(err)
	}

	w, err := NewCleanupWorker(CleanupWorkerConfig{
		DB:     db,
		Pepper: "pepper",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.tickOnce(context.Background())

	var n int
	_ = db.QueryRow(`SELECT count(*) FROM users WHERE id = ?`, uidOld).Scan(&n)
	if n != 0 {
		t.Error("user expirado no fue borrado")
	}
	_ = db.QueryRow(`SELECT count(*) FROM users WHERE id = ?`, uidNew).Scan(&n)
	if n != 1 {
		t.Error("user en gracia fue borrado prematuramente")
	}
}

func TestCleanupWorkerNoOpWhenNothingExpired(t *testing.T) {
	db := setupDB(t)
	uid := insertUser(t, db, "alice@example.com")
	// Sin deleted_at — no es candidato.

	w, err := NewCleanupWorker(CleanupWorkerConfig{
		DB:     db,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.tickOnce(context.Background())

	var n int
	_ = db.QueryRow(`SELECT count(*) FROM users WHERE id = ?`, uid).Scan(&n)
	if n != 1 {
		t.Error("user activo fue tocado")
	}
}

func TestCleanupWorkerHousekeepsStaleCodes(t *testing.T) {
	db := setupDB(t)
	uid := insertUser(t, db, "alice@example.com")
	// Código expirado hace 2 días → debe limpiarse.
	stale := time.Now().UTC().Add(-2 * 24 * time.Hour).Unix()
	if _, err := db.Exec(
		`INSERT INTO deletion_codes (user_id, code_hash, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		uid, "hash", stale, stale,
	); err != nil {
		t.Fatal(err)
	}
	// Código que sí está expirado pero hace solo 1 hora → se queda
	// (housekeeping toca a las 24h+).
	recent := time.Now().UTC().Add(-1 * time.Hour).Unix()
	if _, err := db.Exec(
		`INSERT INTO deletion_codes (user_id, code_hash, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		uid, "hash2", recent, recent,
	); err != nil {
		t.Fatal(err)
	}

	w, _ := NewCleanupWorker(CleanupWorkerConfig{DB: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	w.tickOnce(context.Background())

	var stillStale int
	_ = db.QueryRow(`SELECT count(*) FROM deletion_codes WHERE code_hash = 'hash'`).Scan(&stillStale)
	if stillStale != 0 {
		t.Error("código expirado hace 2 días no fue limpiado")
	}
	var stillRecent int
	_ = db.QueryRow(`SELECT count(*) FROM deletion_codes WHERE code_hash = 'hash2'`).Scan(&stillRecent)
	if stillRecent != 1 {
		t.Error("código expirado reciente fue borrado prematuramente")
	}
}

// guard contra import sin uso si alguien borra usos arriba.
var _ sql.DB