package server

import (
	"database/sql"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/session"
)

// htmlTemplatesFS apunta a web/templates/ del repo. Lo usamos en tests para
// instanciar el errorRenderer real sin tener que armar un FS sintético.
func htmlTemplatesFS(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS("../../web/templates")
}

func TestHealth(t *testing.T) {
	s := New(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado %d, obtenido %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: esperado application/json, obtenido %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("body no contiene status ok: %q", rec.Body.String())
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID no fue seteado")
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := New(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)

	cases := map[string]string{
		"Strict-Transport-Security": "max-age=",
		"X-Frame-Options":           "DENY",
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
		"Content-Security-Policy":   "default-src 'self'",
		"Permissions-Policy":        "camera=()",
	}
	for name, wantContains := range cases {
		got := rec.Header().Get(name)
		if !strings.Contains(got, wantContains) {
			t.Errorf("%s: esperado contener %q, obtenido %q", name, wantContains, got)
		}
	}
}

func TestSecurityHeadersIncludeSubDomains(t *testing.T) {
	s := New(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)

	hsts := rec.Header().Get("Strict-Transport-Security")
	if !strings.Contains(hsts, "includeSubDomains") {
		t.Errorf("HSTS no incluye includeSubDomains: %q", hsts)
	}
	if strings.Contains(hsts, "preload") {
		t.Errorf("HSTS no debería tener 'preload' (irreversible): %q", hsts)
	}
}

func TestNotFoundRendersStyledPage(t *testing.T) {
	s := New(Config{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTMLTemplates: htmlTemplatesFS(t),
	})
	req := httptest.NewRequest(http.MethodGet, "/this-path-does-not-exist", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: esperado 404, obtenido %d", rec.Code)
	}
	body := rec.Body.String()
	// Marcadores únicos de la página 404 estilizada (kanji 迷 + eyebrow).
	for _, want := range []string{"迷", "página perdida", "no existe"} {
		if !strings.Contains(body, want) {
			t.Errorf("404 no contiene %q", want)
		}
	}
}

func TestRecovererRenders500WithRequestID(t *testing.T) {
	s := New(Config{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTMLTemplates: htmlTemplatesFS(t),
	})
	// Wrap routes en un handler que paniquea para gatillar el recoverer.
	handler := s.recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	req := httptest.NewRequest(http.MethodGet, "/explode", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: esperado 500, obtenido %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"algo se rompió", "№ 500"} {
		if !strings.Contains(body, want) {
			t.Errorf("500 no contiene %q", want)
		}
	}
}

func TestRecovererFallsBackWhenTemplatesMissing(t *testing.T) {
	// Sin HTMLTemplates, el recoverer debe usar el http.Error plano y no
	// tirar nil-deref. Es el caso de tests viejos que no pasan assets.
	s := New(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	handler := s.recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	req := httptest.NewRequest(http.MethodGet, "/explode", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: esperado 500, obtenido %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Errorf("fallback plano no se renderizó: %q", rec.Body.String())
	}
}

// setupTestDB crea una DB en memoria con las migrations aplicadas.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrations.Run(db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("aplicar migrations: %v", err)
	}
	return db
}

func insertTestUser(t *testing.T, db *sql.DB, email string, onboardedAt sql.NullInt64) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO users (email, created_at, onboarded_at) VALUES (?, ?, ?)`,
		email, time.Now().Unix(), onboardedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// newOnboardingRedirectServer construye un Server mínimo con la DB de test
// y sin Sessions (para inyectar la sesión directamente en el context).
func newOnboardingRedirectServer(t *testing.T, db *sql.DB) *Server {
	t.Helper()
	return New(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:     db,
	})
}

func TestOnboardingRedirect_NullOnboardedAt(t *testing.T) {
	db := setupTestDB(t)
	uid := insertTestUser(t, db, "nob@example.com", sql.NullInt64{})
	s := newOnboardingRedirectServer(t, db)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := s.onboardingRedirect(inner)

	req := httptest.NewRequest(http.MethodGet, "/library", nil)
	req = session.WithUserID(req, uid)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: esperado 303, obtenido %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/onboarding" {
		t.Errorf("Location: esperado /onboarding, obtenido %q", loc)
	}
}

func TestOnboardingRedirect_PathExcluded(t *testing.T) {
	db := setupTestDB(t)
	uid := insertTestUser(t, db, "nob2@example.com", sql.NullInt64{})
	s := newOnboardingRedirectServer(t, db)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := s.onboardingRedirect(inner)

	for _, path := range []string{"/auth/logout", "/onboarding", "/onboarding/step2", "/version"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = session.WithUserID(req, uid)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("path %s: esperado 200 (pass-through), obtenido %d", path, rec.Code)
		}
	}
}

func TestOnboardingRedirect_AlreadyOnboarded(t *testing.T) {
	db := setupTestDB(t)
	uid := insertTestUser(t, db, "onb@example.com", sql.NullInt64{Valid: true, Int64: time.Now().Unix()})
	s := newOnboardingRedirectServer(t, db)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := s.onboardingRedirect(inner)

	req := httptest.NewRequest(http.MethodGet, "/library", nil)
	req = session.WithUserID(req, uid)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200 (pass-through), obtenido %d", rec.Code)
	}
}
