package home

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/session"
)

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, name := range []string{
		"../../migrations/0001_users_and_sessions.sql",
		"../../migrations/0002_otp_codes.sql",
	} {
		bs, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("leer %s: %v", name, err)
		}
		if _, err := db.Exec(string(bs)); err != nil {
			t.Fatalf("aplicar %s: %v", name, err)
		}
	}
	return db
}

func insertUser(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO users (email, created_at) VALUES (?, ?)`, email, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newTestHandler(t *testing.T, db *sql.DB) *Handler {
	t.Helper()
	h, err := New(Config{
		DB:            db,
		HTMLTemplates: os.DirFS("../../web/templates"),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func TestHomeRedirectsAnonymous(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.home(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: esperado 303, obtenido %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/auth" {
		t.Errorf("location: esperado /auth, obtenido %q", loc)
	}
}

func TestHomeRendersForLoggedInUser(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)

	userID := insertUser(t, db, "alice@example.com")
	s := &session.Session{
		UserID:    userID,
		CSRFToken: "csrf-token-123",
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(session.NewContext(context.Background(), s))
	rec := httptest.NewRecorder()
	h.home(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: esperado 200, obtenido %d\nbody: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice@example.com") {
		t.Error("email no aparece en el body")
	}
	if !strings.Contains(body, `action="/auth/logout"`) {
		t.Error("form de logout no aparece")
	}
	if !strings.Contains(body, `name="csrf_token" value="csrf-token-123"`) {
		t.Error("CSRF input no fue renderizado")
	}
}

func TestHomeHandlesMissingUser(t *testing.T) {
	// Session apunta a un user_id que no existe (caso de corrupción/cleanup).
	db := setupDB(t)
	h := newTestHandler(t, db)

	s := &session.Session{UserID: 9999, CSRFToken: "x"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(session.NewContext(context.Background(), s))
	rec := httptest.NewRecorder()
	h.home(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: esperado 500, obtenido %d", rec.Code)
	}
}
