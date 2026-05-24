package digest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkerSkipsOffWindow(t *testing.T) {
	db := setupDB(t)
	sender, cap := newTestSender(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "M")
	insertEntry(t, db, uid, mid, "reading")
	insertChapter(t, db, mid, "ch-1", "1", "en", time.Now().Add(-1*time.Hour))

	w, err := NewWorker(WorkerConfig{DB: db, Sender: sender})
	if err != nil {
		t.Fatal(err)
	}
	// Forzamos "ahora" a lunes 10am UTC: fuera de la ventana.
	monday := time.Date(2025, 6, 2, 10, 0, 0, 0, time.UTC) // lunes
	w.now = func() time.Time { return monday }

	w.tickIfDue(context.Background())

	if cap.count() != 0 {
		t.Errorf("se mandó email fuera de ventana: %d", cap.count())
	}
}

func TestWorkerFiresInWindow(t *testing.T) {
	db := setupDB(t)
	sender, cap := newTestSender(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "M")
	insertEntry(t, db, uid, mid, "reading")
	insertChapter(t, db, mid, "ch-1", "1", "en", time.Now().Add(-1*time.Hour))

	w, err := NewWorker(WorkerConfig{DB: db, Sender: sender})
	if err != nil {
		t.Fatal(err)
	}
	sunday := time.Date(2025, 6, 1, 9, 30, 0, 0, time.UTC) // domingo 9:30am
	w.now = func() time.Time { return sunday }

	w.tickIfDue(context.Background())

	if cap.count() != 1 {
		t.Errorf("se esperaba 1 email, obtuve %d", cap.count())
	}
}

func TestWorkerSkipsDisabledUsers(t *testing.T) {
	db := setupDB(t)
	sender, cap := newTestSender(t, db)

	// Dos users: uno opt-in, uno opt-out.
	uidIn := insertUser(t, db, "alice@example.com", 1)
	uidOut := insertUser(t, db, "bob@example.com", 0)
	mid := insertManga(t, db, "M")
	insertEntry(t, db, uidIn, mid, "reading")
	insertEntry(t, db, uidOut, mid, "reading")
	insertChapter(t, db, mid, "ch-1", "1", "en", time.Now().Add(-1*time.Hour))

	w, _ := NewWorker(WorkerConfig{DB: db, Sender: sender})
	w.now = func() time.Time {
		return time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC)
	}

	w.tickIfDue(context.Background())

	if cap.count() != 1 {
		t.Errorf("se esperaba 1 email (solo opt-in), obtuve %d", cap.count())
	}
	if len(cap.to) != 1 || cap.to[0] != "alice@example.com" {
		t.Errorf("se mandó a destinatarios inesperados: %v", cap.to)
	}
}

func TestWorkerSkipsDeletedUsers(t *testing.T) {
	db := setupDB(t)
	sender, cap := newTestSender(t, db)

	uid := insertUser(t, db, "alice@example.com", 1)
	if _, err := db.Exec(`UPDATE users SET deleted_at = 1 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	mid := insertManga(t, db, "M")
	insertEntry(t, db, uid, mid, "reading")
	insertChapter(t, db, mid, "ch-1", "1", "en", time.Now().Add(-1*time.Hour))

	w, _ := NewWorker(WorkerConfig{DB: db, Sender: sender})
	w.now = func() time.Time {
		return time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC)
	}

	w.tickIfDue(context.Background())

	if cap.count() != 0 {
		t.Errorf("se mandó a user borrado: %d", cap.count())
	}
}

func TestWorkerSecondTickInWindowIsIdempotent(t *testing.T) {
	// Dos ticks consecutivos dentro de la misma ventana: el segundo no
	// debe duplicar porque last_digest_sent_at se actualizó.
	db := setupDB(t)
	sender, cap := newTestSender(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "M")
	insertEntry(t, db, uid, mid, "reading")
	insertChapter(t, db, mid, "ch-1", "1", "en", time.Now().Add(-1*time.Hour))

	w, _ := NewWorker(WorkerConfig{DB: db, Sender: sender})
	w.now = func() time.Time {
		return time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC)
	}

	w.tickIfDue(context.Background())
	w.tickIfDue(context.Background())

	if cap.count() != 1 {
		t.Errorf("doble tick duplicó: %d emails", cap.count())
	}
}

func TestWorkerFiresLaterInDayAfterBoot(t *testing.T) {
	// Recovery: el binario arrancó después de las 9 UTC del domingo (ej.
	// reinicio mid-día tras un deploy). Antes esto perdía la semana entera;
	// ahora el tick a las 14 UTC dispara porque no hay digest_runs del día.
	db := setupDB(t)
	sender, cap := newTestSender(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "M")
	insertEntry(t, db, uid, mid, "reading")
	insertChapter(t, db, mid, "ch-1", "1", "en", time.Now().Add(-1*time.Hour))

	w, _ := NewWorker(WorkerConfig{DB: db, Sender: sender})
	w.now = func() time.Time {
		return time.Date(2025, 6, 1, 14, 0, 0, 0, time.UTC) // domingo 14:00
	}
	w.tickIfDue(context.Background())

	if cap.count() != 1 {
		t.Errorf("recovery tras 9 UTC no disparó: %d emails", cap.count())
	}
}

func TestWorkerRetriesAfterFailedRun(t *testing.T) {
	// El primer tick falla SMTP para el user; el segundo tick (mismo
	// día, dentro del cap) reintenta porque el último digest_run tiene
	// failed>0 y last_digest_sent_at no avanzó.
	db := setupDB(t)
	sender, cap := newTestSender(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "M")
	insertEntry(t, db, uid, mid, "reading")
	insertChapter(t, db, mid, "ch-1", "1", "en", time.Now().Add(-1*time.Hour))

	w, _ := NewWorker(WorkerConfig{DB: db, Sender: sender})
	w.now = func() time.Time {
		return time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC)
	}

	// 1er tick: 2 errores consecutivos → SendToUser falla incluso con retry interno.
	cap.errs = []error{errors.New("smtp down 1"), errors.New("smtp down 2")}
	w.tickIfDue(context.Background())
	if cap.count() != 0 {
		t.Fatalf("primer tick no debería haber entregado: %d", cap.count())
	}

	// 2do tick: sin errores planeados → SMTP recuperado, retry diferido envía.
	w.tickIfDue(context.Background())
	if cap.count() != 1 {
		t.Errorf("retry diferido no entregó: %d", cap.count())
	}

	// Hay 2 filas en digest_runs (primera con failed=1, segunda con sent=1).
	var runs, lastFailed, lastSent int
	if err := db.QueryRow(`SELECT COUNT(*) FROM digest_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Errorf("esperaba 2 digest_runs, hay %d", runs)
	}
	if err := db.QueryRow(`SELECT failed, sent FROM digest_runs ORDER BY id DESC LIMIT 1`).Scan(&lastFailed, &lastSent); err != nil {
		t.Fatal(err)
	}
	if lastFailed != 0 || lastSent != 1 {
		t.Errorf("último run debería tener failed=0 sent=1, obtuve failed=%d sent=%d", lastFailed, lastSent)
	}
}

func TestWorkerSkipsWhenLastRunSucceeded(t *testing.T) {
	// Si el último digest_run del día completó sin failures, ticks
	// posteriores son no-op (no quemamos CPU re-procesando users).
	db := setupDB(t)
	sender, cap := newTestSender(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "M")
	insertEntry(t, db, uid, mid, "reading")
	insertChapter(t, db, mid, "ch-1", "1", "en", time.Now().Add(-1*time.Hour))

	w, _ := NewWorker(WorkerConfig{DB: db, Sender: sender})
	w.now = func() time.Time {
		return time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC)
	}

	w.tickIfDue(context.Background())                           // 1er tick: dispatch OK
	w.now = func() time.Time { return time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC) }
	w.tickIfDue(context.Background())                           // 2do tick: skip
	w.now = func() time.Time { return time.Date(2025, 6, 1, 22, 0, 0, 0, time.UTC) }
	w.tickIfDue(context.Background())                           // 3er tick (último de ventana): skip

	if cap.count() != 1 {
		t.Errorf("ticks adicionales con run limpio re-dispatcharon: %d", cap.count())
	}
}

func TestWorkerHonorsMaxRunsPerDay(t *testing.T) {
	// Con cap=2, después de 2 runs con failures el worker NO sigue
	// reintentando aunque siga la ventana. Evita thrash si SMTP cae
	// completo todo el domingo.
	db := setupDB(t)
	sender, cap := newTestSender(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "M")
	insertEntry(t, db, uid, mid, "reading")
	insertChapter(t, db, mid, "ch-1", "1", "en", time.Now().Add(-1*time.Hour))

	w, _ := NewWorker(WorkerConfig{DB: db, Sender: sender, MaxRunsPerDay: 2})
	w.now = func() time.Time {
		return time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC)
	}

	// 2 ticks con SMTP siempre roto → 2 runs con failed=1 cada uno.
	cap.err = errors.New("smtp permanently down")
	w.tickIfDue(context.Background())
	w.tickIfDue(context.Background())
	// 3er tick: el cap ya está alcanzado, no debe disparar.
	w.tickIfDue(context.Background())

	var runs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM digest_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Errorf("esperaba 2 digest_runs (cap=2), hay %d", runs)
	}
}

func TestWorkerPersistsCountersInDigestRun(t *testing.T) {
	// Tras un dispatch, la fila de digest_runs tiene los counters
	// poblados y finished_at no-NULL.
	db := setupDB(t)
	sender, _ := newTestSender(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "M")
	insertEntry(t, db, uid, mid, "reading")
	insertChapter(t, db, mid, "ch-1", "1", "en", time.Now().Add(-1*time.Hour))

	w, _ := NewWorker(WorkerConfig{DB: db, Sender: sender})
	w.now = func() time.Time {
		return time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC)
	}
	w.tickIfDue(context.Background())

	var candidates, sent, skipped, failed int
	var finishedAt *int64
	if err := db.QueryRow(
		`SELECT candidates, sent, skipped, failed, finished_at FROM digest_runs ORDER BY id DESC LIMIT 1`,
	).Scan(&candidates, &sent, &skipped, &failed, &finishedAt); err != nil {
		t.Fatal(err)
	}
	if candidates != 1 || sent != 1 || skipped != 0 || failed != 0 {
		t.Errorf("counters incorrectos: candidates=%d sent=%d skipped=%d failed=%d",
			candidates, sent, skipped, failed)
	}
	if finishedAt == nil {
		t.Error("finished_at debería estar seteado al final del run")
	}
}
