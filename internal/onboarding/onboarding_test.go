package onboarding

import (
	"context"
	"database/sql"
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
	if _, err := migrations.Run(db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("aplicar migrations: %v", err)
	}
	return db
}

func insertUser(t *testing.T, db *sql.DB, email string, onboardedAt sql.NullInt64) int64 {
	t.Helper()
	now := time.Now().Unix()
	res, err := db.Exec(
		`INSERT INTO users (email, created_at, onboarded_at) VALUES (?, ?, ?)`,
		email, now, onboardedAt,
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

func newTestHandler(t *testing.T, db *sql.DB) *Handler {
	t.Helper()
	h, err := New(Config{
		DB:            db,
		HTMLTemplates: os.DirFS("../../web/templates"),
	})
	if err != nil {
		t.Fatalf("crear handler: %v", err)
	}
	return h
}

func withSession(req *http.Request, userID int64) *http.Request {
	sess := &session.Session{UserID: userID}
	ctx := session.NewContext(req.Context(), sess)
	return req.WithContext(ctx)
}

func TestStep1_RedirectsToAuthWithoutSession(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	req := httptest.NewRequest(http.MethodGet, "/onboarding", nil)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.Mount(mux)
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, esperado 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth" {
		t.Errorf("Location = %q, esperado /auth", loc)
	}
}

func TestStep1_RedirectsToLibraryWhenOnboarded(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	now := time.Now().Unix()
	uid := insertUser(t, db, "a@example.com", sql.NullInt64{Int64: now, Valid: true})
	req := withSession(httptest.NewRequest(http.MethodGet, "/onboarding", nil), uid)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.Mount(mux)
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, esperado 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/library" {
		t.Errorf("Location = %q, esperado /library", loc)
	}
}

func TestStep1_RendersWhenNotOnboarded(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	uid := insertUser(t, db, "a@example.com", sql.NullInt64{})
	req := withSession(httptest.NewRequest(http.MethodGet, "/onboarding", nil), uid)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.Mount(mux)
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200. Body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cataloga") {
		t.Errorf("body no contiene 'cataloga'")
	}
}

func TestFinish_WritesOnboardedAtAndFrequency(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	uid := insertUser(t, db, "a@example.com", sql.NullInt64{})
	form := strings.NewReader("digest_frequency=daily")
	req := withSession(httptest.NewRequest(http.MethodPost, "/onboarding/finish", form), uid)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.Mount(mux)
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, esperado 303. Body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/library" {
		t.Errorf("Location = %q, esperado /library", loc)
	}
	var freq string
	var onboardedAt sql.NullInt64
	err := db.QueryRowContext(context.Background(),
		`SELECT digest_frequency, onboarded_at FROM users WHERE id = ?`, uid,
	).Scan(&freq, &onboardedAt)
	if err != nil {
		t.Fatal(err)
	}
	if freq != "daily" {
		t.Errorf("digest_frequency = %q, esperado 'daily'", freq)
	}
	if !onboardedAt.Valid {
		t.Errorf("onboarded_at debería estar seteado, está NULL")
	}
}

func TestStep1_FullScreenNoChromeWithSkip(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com", sql.NullInt64{}) // no onboardeado

	req := httptest.NewRequest(http.MethodGet, "/onboarding", nil)
	req = withSession(req, userID)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.Mount(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", w.Code)
	}
	body := w.Body.String()

	// Takeover full-screen: ninguno de los elementos HTML del chrome.
	// (Chequeamos tags, no clases sueltas — las clases también viven en el CSS.)
	for _, forbidden := range []string{`<aside`, `<header class="app-nav"`, `<footer class="app-footer"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("onboarding renderiza chrome %q; debe ser full-screen", forbidden)
		}
	}
	if !strings.Contains(body, `action="/onboarding/finish"`) {
		t.Error("falta el form de skip a /onboarding/finish")
	}
	if !strings.Contains(body, "saltar") {
		t.Error("falta el texto de skip 'saltar'")
	}
}

func TestFinish_InvalidFrequencyDefaultsToWeekly(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	uid := insertUser(t, db, "a@example.com", sql.NullInt64{})
	form := strings.NewReader("digest_frequency=invalid")
	req := withSession(httptest.NewRequest(http.MethodPost, "/onboarding/finish", form), uid)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.Mount(mux)
	mux.ServeHTTP(w, req)
	var freq string
	err := db.QueryRowContext(context.Background(),
		`SELECT digest_frequency FROM users WHERE id = ?`, uid,
	).Scan(&freq)
	if err != nil {
		t.Fatal(err)
	}
	if freq != "weekly" {
		t.Errorf("digest_frequency = %q, esperado 'weekly' (safe default)", freq)
	}
}

func TestSteps2And3_FullScreenNoChrome(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com", sql.NullInt64{}) // no onboardeado
	mux := http.NewServeMux()
	h.Mount(mux)

	for _, path := range []string{"/onboarding/step2", "/onboarding/step3"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = withSession(req, userID)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("%s: status esperado 200, obtenido %d", path, w.Code)
		}
		body := w.Body.String()
		for _, forbidden := range []string{`<aside`, `<header class="app-nav"`, `<footer class="app-footer"`} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s: renderiza chrome %q; debe ser full-screen", path, forbidden)
			}
		}
	}
}

// D8: el paso 2 nombra las sources habilitadas en esta instancia, no una
// lista escrita a mano.
func TestOnboardingStep2ListsConfiguredSources(t *testing.T) {
	db := setupDB(t)
	h, err := New(Config{
		DB:            db,
		HTMLTemplates: os.DirFS("../../web/templates"),
		SourceNames:   []string{"asura", "comick", "mangadex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	userID := insertUser(t, db, "alice@example.com", sql.NullInt64{})
	mux := http.NewServeMux()
	h.Mount(mux)
	req := withSession(httptest.NewRequest(http.MethodGet, "/onboarding/step2", nil), userID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), "asura, comick y mangadex") {
		t.Error("el paso 2 no lista las sources configuradas")
	}
}
