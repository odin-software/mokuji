package profile

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/session"
)

type capturedEmail struct {
	to      string
	subject string
	body    string
}

type emailCapture struct {
	mu   sync.Mutex
	sent []capturedEmail
}

func (e *emailCapture) fn(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Parseo brutal: extraer Subject: line + body después de \r\n\r\n.
	parts := strings.SplitN(string(msg), "\r\n\r\n", 2)
	subject := ""
	if i := strings.Index(parts[0], "Subject: "); i >= 0 {
		subject = parts[0][i+len("Subject: "):]
		if j := strings.Index(subject, "\r\n"); j >= 0 {
			subject = subject[:j]
		}
	}
	body := ""
	if len(parts) > 1 {
		body = parts[1]
	}
	e.sent = append(e.sent, capturedEmail{
		to:      to[0],
		subject: subject,
		body:    body,
	})
	return nil
}

func newDeleteHandler(t *testing.T, db *sql.DB) (*Handler, *emailCapture) {
	t.Helper()
	cap := &emailCapture{}
	cli, err := email.New(email.Config{
		Host: "localhost",
		Port: 1025,
		From: "mokuji <auth@mokuji.local>",
	})
	if err != nil {
		t.Fatal(err)
	}
	cli.SetSendMailForTesting(cap.fn)

	h, err := New(Config{
		DB:             db,
		HTMLTemplates:  os.DirFS("../../web/templates"),
		EmailTemplates: os.DirFS("../../web/emails"),
		Email:          cli,
		BaseURL:        "https://mokuji.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, cap
}

func TestDeleteAccountRequestSendsEmailAndStoresCode(t *testing.T) {
	db := setupDB(t)
	h, cap := newDeleteHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")

	r := httptest.NewRequest("POST", "/settings/delete-account/request", strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(session.NewContext(context.Background(), &session.Session{UserID: uid}))
	w := httptest.NewRecorder()
	h.deleteAccountRequest(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status: %d body: %s", w.Code, w.Body.String())
	}
	if len(cap.sent) != 1 {
		t.Fatalf("emails enviados: %d", len(cap.sent))
	}
	if cap.sent[0].to != "alice@example.com" {
		t.Errorf("to: %q", cap.sent[0].to)
	}
	if !strings.Contains(cap.sent[0].body, "borrar tu cuenta") {
		t.Errorf("body no menciona el borrado: %s", cap.sent[0].body)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM deletion_codes WHERE user_id = ?`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("deletion_codes filas: %d", n)
	}
}

func TestDeleteAccountConfirmMarksDeletedAt(t *testing.T) {
	db := setupDB(t)
	h, _ := newDeleteHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")

	// Inyectamos código directo en DB para no depender del email.
	code := "123456"
	if err := h.storeDeletionCode(context.Background(), uid, code); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"code": {code}}
	r := httptest.NewRequest("POST", "/settings/delete-account/confirm", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(session.NewContext(context.Background(), &session.Session{UserID: uid}))
	w := httptest.NewRecorder()
	h.deleteAccountConfirm(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status: %d body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/?deleted=1") {
		t.Errorf("Location: %q", loc)
	}

	var deletedAt sql.NullInt64
	if err := db.QueryRow(`SELECT deleted_at FROM users WHERE id = ?`, uid).Scan(&deletedAt); err != nil {
		t.Fatal(err)
	}
	if !deletedAt.Valid {
		t.Error("deleted_at no fue seteado")
	}
}

func TestDeleteAccountConfirmRejectsWrongCode(t *testing.T) {
	db := setupDB(t)
	h, _ := newDeleteHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")
	if err := h.storeDeletionCode(context.Background(), uid, "123456"); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"code": {"999999"}}
	r := httptest.NewRequest("POST", "/settings/delete-account/confirm", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(session.NewContext(context.Background(), &session.Session{UserID: uid}))
	w := httptest.NewRecorder()
	h.deleteAccountConfirm(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: %d", w.Code)
	}
	var deletedAt sql.NullInt64
	_ = db.QueryRow(`SELECT deleted_at FROM users WHERE id = ?`, uid).Scan(&deletedAt)
	if deletedAt.Valid {
		t.Error("código incorrecto disparó borrado")
	}
}

func TestDeleteAccountConfirmRejectsExpiredCode(t *testing.T) {
	db := setupDB(t)
	h, _ := newDeleteHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")
	// Inserta directo un código ya expirado.
	past := time.Now().UTC().Add(-1 * time.Hour).Unix()
	if _, err := db.Exec(
		`INSERT INTO deletion_codes (user_id, code_hash, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		uid, hashDeletionCode("123456"), past, past,
	); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"code": {"123456"}}
	r := httptest.NewRequest("POST", "/settings/delete-account/confirm", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(session.NewContext(context.Background(), &session.Session{UserID: uid}))
	w := httptest.NewRecorder()
	h.deleteAccountConfirm(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: %d", w.Code)
	}
	var deletedAt sql.NullInt64
	_ = db.QueryRow(`SELECT deleted_at FROM users WHERE id = ?`, uid).Scan(&deletedAt)
	if deletedAt.Valid {
		t.Error("código expirado disparó borrado")
	}
}

func TestDeleteAccountConfirmIsSingleUse(t *testing.T) {
	db := setupDB(t)
	h, _ := newDeleteHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")
	code := "123456"
	if err := h.storeDeletionCode(context.Background(), uid, code); err != nil {
		t.Fatal(err)
	}

	// Primer consume OK.
	ok, err := h.consumeDeletionCode(context.Background(), uid, code)
	if err != nil || !ok {
		t.Fatalf("primer consume: ok=%v err=%v", ok, err)
	}
	// Segundo consume: ya gastado.
	ok, err = h.consumeDeletionCode(context.Background(), uid, code)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("código gastado fue aceptado de nuevo")
	}
}

func TestRestoreAccountCancelClearsDeletedAt(t *testing.T) {
	db := setupDB(t)
	h, _ := newDeleteHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")
	now := time.Now().UTC().Unix()
	if _, err := db.Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, now, uid); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("POST", "/restore-account/cancel", strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(session.NewContext(context.Background(), &session.Session{UserID: uid}))
	w := httptest.NewRecorder()
	h.restoreAccountCancel(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status: %d", w.Code)
	}
	var deletedAt sql.NullInt64
	_ = db.QueryRow(`SELECT deleted_at FROM users WHERE id = ?`, uid).Scan(&deletedAt)
	if deletedAt.Valid {
		t.Error("deleted_at no fue limpiado")
	}
}

func TestRestoreAccountDeleteNowDoesHardDelete(t *testing.T) {
	db := setupDB(t)
	h, _ := newDeleteHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")
	now := time.Now().UTC().Unix()
	if _, err := db.Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, now, uid); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("POST", "/restore-account/delete-now", strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(session.NewContext(context.Background(), &session.Session{UserID: uid}))
	w := httptest.NewRecorder()
	h.restoreAccountDeleteNow(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status: %d", w.Code)
	}
	// User borrado completamente.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM users WHERE id = ?`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("user no fue hard-deleted")
	}
	// Entry en deletion_log.
	var logged int
	if err := db.QueryRow(`SELECT count(*) FROM deletion_log`).Scan(&logged); err != nil {
		t.Fatal(err)
	}
	if logged != 1 {
		t.Errorf("deletion_log entries: %d", logged)
	}
}

func TestDeleteAccountRequestRequiresSession(t *testing.T) {
	db := setupDB(t)
	h, _ := newDeleteHandler(t, db)

	r := httptest.NewRequest("POST", "/settings/delete-account/request", strings.NewReader(""))
	w := httptest.NewRecorder()
	h.deleteAccountRequest(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status: %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth" {
		t.Errorf("redirect: %q", loc)
	}
}

func TestDeleteAccountRequestServiceUnavailableWithoutEmail(t *testing.T) {
	db := setupDB(t)
	h, _ := newDeleteHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")
	h.email = nil // simula construcción sin Email

	r := httptest.NewRequest("POST", "/settings/delete-account/request", strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(session.NewContext(context.Background(), &session.Session{UserID: uid}))
	w := httptest.NewRecorder()
	h.deleteAccountRequest(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: %d", w.Code)
	}
}

