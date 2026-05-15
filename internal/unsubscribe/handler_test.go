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
	if !strings.Contains(body, "link inválido") {
		t.Errorf("no muestra error de link: %s", body)
	}
}

func TestGetPageMissingToken(t *testing.T) {
	db := setupDB(t)
	h, _ := newTestHandler(t, db)

	r := httptest.NewRequest("GET", "/unsubscribe", nil)
	w := httptest.NewRecorder()
	h.page(w, r)

	body := w.Body.String()
	if !strings.Contains(body, "link inválido") {
		t.Errorf("no muestra error de link: %s", body)
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

	if !strings.Contains(w.Body.String(), "link inválido") {
		t.Errorf("no muestra error: %s", w.Body.String())
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
