package digest

import (
	"context"
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
