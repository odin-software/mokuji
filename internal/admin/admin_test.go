package admin

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/releases/digest"
	"git.odin.do/kengru/mokuji/internal/unsubscribe"
)

type captureSMTP struct {
	mu    sync.Mutex
	count int
}

func (c *captureSMTP) fn(string, smtp.Auth, string, []string, []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	return nil
}

func setup(t *testing.T) (*Handler, *sql.DB, *captureSMTP) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrations.Run(db, os.DirFS("../../migrations")); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cap := &captureSMTP{}

	cli, _ := email.New(email.Config{
		Host:   "localhost",
		Port:   1025,
		From:   "mokuji <auth@mokuji.local>",
		Logger: logger,
	})
	cli.SetSendMailForTesting(cap.fn)

	signer, _ := unsubscribe.NewSigner(make([]byte, 32))
	sender, err := digest.New(digest.Config{
		DB:      db,
		Email:   cli,
		Signer:  signer,
		Emails:  os.DirFS("../../web/emails"),
		BaseURL: "https://mokuji.test",
		Logger:  logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	h, err := New(Config{
		DB:     db,
		Token:  "secreto",
		Digest: sender,
		Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, db, cap
}

func insertUserWithCaps(t *testing.T, db *sql.DB, email string, digestEnabled int) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO users (email, created_at, email_digest_enabled, preferred_language)
		 VALUES (?, 0, ?, 'en')`,
		email, digestEnabled,
	)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := res.LastInsertId()

	res, err = db.Exec(`INSERT INTO manga (title, source, source_id, created_at, updated_at) VALUES ('M', 'test', ?, 0, 0)`, email)
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := res.LastInsertId()

	if _, err := db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at) VALUES (?, ?, 'reading', 0, 0)`, uid, mid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chapter (manga_id, source, source_id, number, language, published_at, external_url, created_at) VALUES (?, 'mangadex', 'ch-1', '1', 'en', ?, '', 0)`, mid, time.Now().Add(-1*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	return uid
}

func TestTriggerDigestUnauthorizedWithoutHeader(t *testing.T) {
	h, _, cap := setup(t)

	r := httptest.NewRequest("POST", "/admin/trigger-digest", nil)
	w := httptest.NewRecorder()
	h.triggerDigest(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: %d", w.Code)
	}
	if cap.count != 0 {
		t.Error("se mandó email sin auth")
	}
}

func TestTriggerDigestUnauthorizedWithWrongToken(t *testing.T) {
	h, _, _ := setup(t)

	r := httptest.NewRequest("POST", "/admin/trigger-digest", nil)
	r.Header.Set("Authorization", "Bearer otro")
	w := httptest.NewRecorder()
	h.triggerDigest(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: %d", w.Code)
	}
}

func TestTriggerDigestUnauthorizedWithoutBearerPrefix(t *testing.T) {
	h, _, _ := setup(t)

	r := httptest.NewRequest("POST", "/admin/trigger-digest", nil)
	r.Header.Set("Authorization", "secreto") // sin "Bearer "
	w := httptest.NewRecorder()
	h.triggerDigest(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: %d", w.Code)
	}
}

func TestTriggerDigestDisabledWhenTokenEmpty(t *testing.T) {
	h, _, _ := setup(t)
	h.token = ""

	r := httptest.NewRequest("POST", "/admin/trigger-digest", nil)
	r.Header.Set("Authorization", "Bearer cualquiera")
	w := httptest.NewRecorder()
	h.triggerDigest(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: %d", w.Code)
	}
}

func TestTriggerDigestByUserID(t *testing.T) {
	h, db, cap := setup(t)
	uid := insertUserWithCaps(t, db, "alice@example.com", 1)

	form := url.Values{"user_id": {strconv.FormatInt(uid, 10)}}
	r := httptest.NewRequest("POST", "/admin/trigger-digest", strings.NewReader(form.Encode()))
	r.Header.Set("Authorization", "Bearer secreto")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.triggerDigest(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: %d, body: %s", w.Code, w.Body.String())
	}
	if cap.count != 1 {
		t.Errorf("emails enviados: %d", cap.count)
	}
}

func TestTriggerDigestByEmail(t *testing.T) {
	h, db, cap := setup(t)
	insertUserWithCaps(t, db, "alice@example.com", 1)

	form := url.Values{"email": {"alice@example.com"}}
	r := httptest.NewRequest("POST", "/admin/trigger-digest", strings.NewReader(form.Encode()))
	r.Header.Set("Authorization", "Bearer secreto")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.triggerDigest(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: %d", w.Code)
	}
	if cap.count != 1 {
		t.Errorf("emails: %d", cap.count)
	}
}

func TestTriggerDigestNoChapters(t *testing.T) {
	h, db, _ := setup(t)
	res, _ := db.Exec(
		`INSERT INTO users (email, created_at, email_digest_enabled, preferred_language) VALUES (?, 0, 1, 'en')`,
		"bob@example.com",
	)
	uid, _ := res.LastInsertId()

	form := url.Values{"user_id": {strconv.FormatInt(uid, 10)}}
	r := httptest.NewRequest("POST", "/admin/trigger-digest", strings.NewReader(form.Encode()))
	r.Header.Set("Authorization", "Bearer secreto")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.triggerDigest(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("status: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestTriggerDigestUserDisabled(t *testing.T) {
	h, db, _ := setup(t)
	uid := insertUserWithCaps(t, db, "alice@example.com", 0)

	form := url.Values{"user_id": {strconv.FormatInt(uid, 10)}}
	r := httptest.NewRequest("POST", "/admin/trigger-digest", strings.NewReader(form.Encode()))
	r.Header.Set("Authorization", "Bearer secreto")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.triggerDigest(w, r)

	if w.Code != http.StatusConflict {
		t.Errorf("status: %d", w.Code)
	}
}

func TestTriggerDigestMissingIdentifier(t *testing.T) {
	h, _, _ := setup(t)

	r := httptest.NewRequest("POST", "/admin/trigger-digest", strings.NewReader(""))
	r.Header.Set("Authorization", "Bearer secreto")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.triggerDigest(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: %d", w.Code)
	}
}
