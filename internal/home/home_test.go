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

	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/session"
)

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

func TestHome_UnauthShowsLanding(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	h.Mount(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200 (landing). Body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Una biblioteca") {
		t.Errorf("body no contiene el hero copy 'Una biblioteca'. Body:\n%s", body)
	}
	if !strings.Contains(body, `href="/auth"`) {
		t.Errorf("body no contiene CTA con href=/auth")
	}
	if w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, esperado text/html; charset=utf-8", w.Header().Get("Content-Type"))
	}
}

// D4: con sesión no hay dashboard; la biblioteca es la entrada.
func TestHomeRedirectsLoggedInToLibrary(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID}))
	rec := httptest.NewRecorder()
	h.home(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: esperado 303, obtenido %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/library" {
		t.Errorf("Location: esperado /library, obtenido %q", loc)
	}
}

func TestHome_LandingHasCanonicalAndJSONLD(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	h.Mount(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200", w.Code)
	}
	body := w.Body.String()
	for _, needle := range []string{
		`<link rel="canonical" href="https://mokuji.org">`,
		`<script type="application/ld+json">`,
		`"@type": "SoftwareApplication"`,
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("landing body missing %q", needle)
		}
	}
}

func TestHome_PrivacyPageRenders(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)

	// No session needed — privacy es pública.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/privacy", nil)

	mux := http.NewServeMux()
	h.Mount(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, needle := range []string{
		"política de privacidad",
		"kg@odin.do",
		"7 días de gracia",
		"mokuji.org",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("body missing %q", needle)
		}
	}
}

func TestHome_LandingHasCriticalSections(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	h.Mount(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200", w.Code)
	}
	body := w.Body.String()

	// El test depende de strings concretos. Si el copy del landing cambia,
	// actualizar el test. Es deliberado — el copy es producto, queremos
	// que cambios pasen por revisión.
	checks := map[string]string{
		"hero copy":          "Una biblioteca",
		"hero accent":        "para tu manga.",
		"primary CTA target": `href="/auth"`,
		"feature 01 kanji":   "登",
		"feature 02 kanji":   "新",
		"feature 03 kanji":   "蔵",
		"feature 02 sources": "MangaDex y Asura",
		"anti-feature":       "lo que no hace",
		"closing CTA":        "Cataloga tu primera",
		"footer tagline":     "table of contents, not the book",
		"og image meta":      "og-default.png",
		"twitter card":       "summary_large_image",
	}
	for name, needle := range checks {
		if !strings.Contains(body, needle) {
			t.Errorf("landing no contiene %s (esperaba %q)", name, needle)
		}
	}
}
