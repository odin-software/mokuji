package home

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/migrations"
	releaseshandler "git.odin.do/kengru/mokuji/internal/releases/handler"
	"git.odin.do/kengru/mokuji/internal/session"
)

type stubReleases struct {
	releases []releaseshandler.Release
	err      error
	gotLimit int
}

func (s *stubReleases) UnreadReleases(_ context.Context, _ int64, limit int) ([]releaseshandler.Release, error) {
	s.gotLimit = limit
	return s.releases, s.err
}

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Aplicamos TODAS las migrations: el home handler tocó campos nuevos
	// (slug en 0011) que requieren el schema completo. Antes el test solo
	// corría 0001+0002 — más rápido pero frágil a nuevas columnas.
	if _, err := migrations.Run(db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("aplicar migrations: %v", err)
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
	return newTestHandlerWithReleases(t, db, nil)
}

func newTestHandlerWithReleases(t *testing.T, db *sql.DB, rp ReleasesProvider) *Handler {
	t.Helper()
	h, err := New(Config{
		DB:            db,
		HTMLTemplates: os.DirFS("../../web/templates"),
		Releases:      rp,
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

func TestHomeRendersReleasesWidget(t *testing.T) {
	db := setupDB(t)
	stub := &stubReleases{
		releases: []releaseshandler.Release{
			{
				ChapterID:     42,
				ChapterNumber: "100",
				ChapterTitle:  "El final",
				MangaTitle:    "Test Manga",
				ReaderURL:     "https://example.com/reader/42",
				PublishedAt:   time.Unix(1700000000, 0).UTC(),
			},
		},
	}
	h := newTestHandlerWithReleases(t, db, stub)

	userID := insertUser(t, db, "alice@example.com")
	s := &session.Session{UserID: userID, CSRFToken: "csrf-token-123"}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(session.NewContext(context.Background(), s))
	rec := httptest.NewRecorder()
	h.home(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d\nbody: %s", rec.Code, rec.Body.String())
	}
	if stub.gotLimit != HomeWidgetReleases {
		t.Errorf("limit: esperado %d, obtenido %d", HomeWidgetReleases, stub.gotLimit)
	}
	body := rec.Body.String()
	for _, want := range []string{"últimos releases", "Test Manga", "ver todos", "/releases/mark-read"} {
		if !strings.Contains(body, want) {
			t.Errorf("falta %q en el body", want)
		}
	}
}

func TestHomeHidesWidgetWhenEmpty(t *testing.T) {
	db := setupDB(t)
	stub := &stubReleases{releases: nil}
	h := newTestHandlerWithReleases(t, db, stub)

	userID := insertUser(t, db, "alice@example.com")
	s := &session.Session{UserID: userID, CSRFToken: "csrf-token-123"}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(session.NewContext(context.Background(), s))
	rec := httptest.NewRecorder()
	h.home(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "últimos releases") {
		t.Error("widget no debería renderizarse cuando no hay releases")
	}
}

func TestHomeSurvivesReleasesError(t *testing.T) {
	// Si la query de releases falla, la home igual carga sin el widget.
	db := setupDB(t)
	stub := &stubReleases{err: errors.New("db boom")}
	h := newTestHandlerWithReleases(t, db, stub)

	userID := insertUser(t, db, "alice@example.com")
	s := &session.Session{UserID: userID, CSRFToken: "csrf-token-123"}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(session.NewContext(context.Background(), s))
	rec := httptest.NewRecorder()
	h.home(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "alice@example.com") {
		t.Error("home no se renderizó pese al fallback")
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
