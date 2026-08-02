package grant

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/migrations"
)

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := migrations.Run(db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("aplicar migrations: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO users (id, email, created_at) VALUES (1, 'alice@example.com', ?)`,
		time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("sembrar user: %v", err)
	}
	return db
}

func TestIssueYConsumeRoundTrip(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	token, err := Issue(ctx, db, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(token) != 64 {
		t.Errorf("esperaba token de 64 hex chars (32 bytes), got %d", len(token))
	}

	userID, err := Consume(ctx, db, token)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if userID != 1 {
		t.Errorf("esperaba user_id 1, got %d", userID)
	}
}

func TestConsumeEsSingleUse(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	token, err := Issue(ctx, db, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := Consume(ctx, db, token); err != nil {
		t.Fatalf("primer Consume: %v", err)
	}

	if _, err := Consume(ctx, db, token); !errors.Is(err, ErrInvalid) {
		t.Errorf("token ya usado fue aceptado de nuevo (err=%v)", err)
	}
}

func TestConsumeRechazaVencido(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	token, err := Issue(ctx, db, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Envejecer el grant a mano en vez de esperar 5 minutos reales.
	past := time.Now().UTC().Add(-time.Minute).Unix()
	if _, err := db.Exec(`UPDATE admin_grants SET expires_at = ?`, past); err != nil {
		t.Fatalf("envejecer grant: %v", err)
	}

	if _, err := Consume(ctx, db, token); !errors.Is(err, ErrInvalid) {
		t.Errorf("token vencido fue aceptado (err=%v)", err)
	}
}

func TestConsumeRechazaTokenInexistente(t *testing.T) {
	db := setupDB(t)

	if _, err := Consume(context.Background(), db, "no-existe"); !errors.Is(err, ErrInvalid) {
		t.Errorf("token inexistente no dio ErrInvalid (err=%v)", err)
	}
}

func TestNoGuardaElPlaintext(t *testing.T) {
	db := setupDB(t)

	token, err := Issue(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	var stored string
	if err := db.QueryRow(`SELECT token_hash FROM admin_grants`).Scan(&stored); err != nil {
		t.Fatalf("leer token_hash: %v", err)
	}
	if stored == token {
		t.Error("el token está en DB en plaintext")
	}
}

func TestCleanupExpiredBorraSoloVencidos(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	if _, err := Issue(ctx, db, 1); err != nil {
		t.Fatalf("Issue vigente: %v", err)
	}
	// Un segundo grant, envejecido.
	old, err := Issue(ctx, db, 1)
	if err != nil {
		t.Fatalf("Issue viejo: %v", err)
	}
	past := time.Now().UTC().Add(-time.Hour).Unix()
	if _, err := db.Exec(
		`UPDATE admin_grants SET expires_at = ? WHERE token_hash = ?`, past, hashToken(old),
	); err != nil {
		t.Fatalf("envejecer grant: %v", err)
	}

	if err := CleanupExpired(ctx, db); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM admin_grants`).Scan(&n); err != nil {
		t.Fatalf("contar: %v", err)
	}
	if n != 1 {
		t.Errorf("esperaba 1 grant vigente tras cleanup, quedaron %d", n)
	}
}
