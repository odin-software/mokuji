package unsubscribe

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"golang.org/x/time/rate"

	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/ratelimit"
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

func newTestHandler(t *testing.T, db *sql.DB) (*Handler, *Signer) {
	t.Helper()
	signer, err := NewSigner(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{
		DB:            db,
		Signer:        signer,
		HTMLTemplates: os.DirFS("../../web/templates"),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("crear handler: %v", err)
	}
	return h, signer
}

func insertUser(t *testing.T, db *sql.DB, email string, digestEnabled int) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO users (email, created_at, email_digest_enabled) VALUES (?, 0, ?)`,
		email, digestEnabled,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func digestEnabled(t *testing.T, db *sql.DB, userID int64) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`SELECT email_digest_enabled FROM users WHERE id = ?`, userID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestGetPageValidToken(t *testing.T) {
	db := setupDB(t)
	h, signer := newTestHandler(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	tok := signer.Sign(uid, ScopeReleasesDigest)

	r := httptest.NewRequest("GET", "/unsubscribe?token="+url.QueryEscape(tok), nil)
	w := httptest.NewRecorder()
	h.page(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "confirmá") && !strings.Contains(body, "darme de baja") {
		t.Errorf("página no muestra confirmación: %s", body)
	}
	// Importante: el GET no debe haber tocado la DB.
	if digestEnabled(t, db, uid) != 1 {
		t.Error("el GET desactivó el digest sin POST")
	}
}

func TestGetPageInvalidToken(t *testing.T) {
	db := setupDB(t)
	h, _ := newTestHandler(t, db)

	r := httptest.NewRequest("GET", "/unsubscribe?token=basura", nil)
	w := httptest.NewRecorder()
	h.page(w, r)

	body := w.Body.String()
	if !strings.Contains(body, "reconocido") {
		t.Errorf("no muestra error de link inválido: %s", body)
	}
}

func TestGetPageMissingToken(t *testing.T) {
	db := setupDB(t)
	h, _ := newTestHandler(t, db)

	r := httptest.NewRequest("GET", "/unsubscribe", nil)
	w := httptest.NewRecorder()
	h.page(w, r)

	body := w.Body.String()
	if !strings.Contains(body, "reconocido") {
		t.Errorf("no muestra error de link inválido: %s", body)
	}
}

func TestPostValidTokenDisablesDigest(t *testing.T) {
	db := setupDB(t)
	h, signer := newTestHandler(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	tok := signer.Sign(uid, ScopeReleasesDigest)

	form := url.Values{"token": {tok}}
	r := httptest.NewRequest("POST", "/unsubscribe", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.submit(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no vas a recibir más") {
		t.Errorf("no muestra confirmación: %s", w.Body.String())
	}
	if got := digestEnabled(t, db, uid); got != 0 {
		t.Errorf("digest sigue activo: %d", got)
	}
}

func TestPostTokenInQueryOneClick(t *testing.T) {
	// RFC 8058: cliente One-Click manda POST con token en query y
	// body "List-Unsubscribe=One-Click". Aceptamos esa forma.
	db := setupDB(t)
	h, signer := newTestHandler(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	tok := signer.Sign(uid, ScopeReleasesDigest)

	body := "List-Unsubscribe=One-Click"
	r := httptest.NewRequest("POST", "/unsubscribe?token="+url.QueryEscape(tok), strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.submit(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: %d", w.Code)
	}
	if got := digestEnabled(t, db, uid); got != 0 {
		t.Errorf("digest sigue activo: %d", got)
	}
}

func TestPostInvalidTokenNoChange(t *testing.T) {
	db := setupDB(t)
	h, _ := newTestHandler(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)

	form := url.Values{"token": {"basura"}}
	r := httptest.NewRequest("POST", "/unsubscribe", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.submit(w, r)

	if !strings.Contains(w.Body.String(), "reconocido") {
		t.Errorf("no muestra error de link inválido: %s", w.Body.String())
	}
	if got := digestEnabled(t, db, uid); got != 1 {
		t.Errorf("digest fue alterado: %d", got)
	}
}

func TestPostIdempotent(t *testing.T) {
	// Un segundo POST con el mismo token (ej. cliente reintenta) sigue
	// retornando "done" sin error y sin efectos colaterales.
	db := setupDB(t)
	h, signer := newTestHandler(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	tok := signer.Sign(uid, ScopeReleasesDigest)

	for i := range 2 {
		form := url.Values{"token": {tok}}
		r := httptest.NewRequest("POST", "/unsubscribe", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.submit(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("iter %d status: %d", i, w.Code)
		}
	}
	if got := digestEnabled(t, db, uid); got != 0 {
		t.Errorf("digest sigue activo: %d", got)
	}
}

func TestPostDeletedUserNoOp(t *testing.T) {
	db := setupDB(t)
	h, signer := newTestHandler(t, db)
	uid := insertUser(t, db, "alice@example.com", 1)
	if _, err := db.Exec(`UPDATE users SET deleted_at = 1 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	tok := signer.Sign(uid, ScopeReleasesDigest)

	form := url.Values{"token": {tok}}
	r := httptest.NewRequest("POST", "/unsubscribe", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.submit(w, r)

	// Respondemos "done" igual (no revelamos estado del user).
	if !strings.Contains(w.Body.String(), "no vas a recibir más") {
		t.Errorf("no muestra confirmación: %s", w.Body.String())
	}
	// El digest sigue como estaba (no se updateó porque deleted_at IS NULL falla).
	if got := digestEnabled(t, db, uid); got != 1 {
		t.Errorf("user borrado fue tocado: %d", got)
	}
}

func TestRateLimitReturns429(t *testing.T) {
	// Limiter con burst=2 y rate ~imposible (1/hora). Las primeras 2
	// requests pasan, la 3era recibe 429. Aplica a GET y POST porque
	// ambos paths consumen tokens (ambos hacen Verify HMAC).
	db := setupDB(t)
	signer, err := NewSigner(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{
		DB:            db,
		Signer:        signer,
		HTMLTemplates: os.DirFS("../../web/templates"),
		IPLimiter:     ratelimit.New(rate.Every(time.Hour), 2),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("crear handler: %v", err)
	}

	// 3 requests desde el mismo RemoteAddr.
	for i := 1; i <= 3; i++ {
		r := httptest.NewRequest("GET", "/unsubscribe?token=x", nil)
		r.RemoteAddr = "203.0.113.5:12345"
		w := httptest.NewRecorder()
		h.page(w, r)

		switch {
		case i <= 2 && w.Code == http.StatusTooManyRequests:
			t.Errorf("req %d: bloqueada antes del burst (status %d)", i, w.Code)
		case i == 3 && w.Code != http.StatusTooManyRequests:
			t.Errorf("req %d: esperaba 429, obtuve %d", i, w.Code)
		}
	}

	// Otro IP no comparte bucket: pasa.
	r := httptest.NewRequest("GET", "/unsubscribe?token=x", nil)
	r.RemoteAddr = "198.51.100.7:12345"
	w := httptest.NewRecorder()
	h.page(w, r)
	if w.Code == http.StatusTooManyRequests {
		t.Errorf("IP distinto fue rate-limited: status %d", w.Code)
	}
}

func TestRateLimitNilDisablesCheck(t *testing.T) {
	// IPLimiter nil = sin rate limiting (útil en tests existentes y para
	// self-hosters paranoicos del rendimiento). Aceptamos 100 GETs sin 429.
	db := setupDB(t)
	h, _ := newTestHandler(t, db) // no setea IPLimiter

	for i := range 100 {
		r := httptest.NewRequest("GET", "/unsubscribe?token=x", nil)
		r.RemoteAddr = "203.0.113.5:12345"
		w := httptest.NewRecorder()
		h.page(w, r)
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("nil limiter rate-limiteó en iter %d", i)
		}
	}
}

func TestRateLimitHonorsTrustProxy(t *testing.T) {
	// TrustProxy=true → buckets por X-Forwarded-For, no por RemoteAddr.
	// Tres requests desde el mismo XFF pero distinto RemoteAddr deben
	// compartir bucket; al 3ero pega el rate limit.
	db := setupDB(t)
	signer, err := NewSigner(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{
		DB:            db,
		Signer:        signer,
		HTMLTemplates: os.DirFS("../../web/templates"),
		IPLimiter:     ratelimit.New(rate.Every(time.Hour), 2),
		TrustProxy:    true,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 3; i++ {
		r := httptest.NewRequest("GET", "/unsubscribe?token=x", nil)
		r.RemoteAddr = "127.0.0.1:0" // simula proxy local
		r.Header.Set("X-Forwarded-For", "203.0.113.5")
		w := httptest.NewRecorder()
		h.page(w, r)
		if i == 3 && w.Code != http.StatusTooManyRequests {
			t.Errorf("req %d con XFF: esperaba 429, obtuve %d", i, w.Code)
		}
	}
}
