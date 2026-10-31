package feedback

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/odin-software/mokuji/internal/email"
	"github.com/odin-software/mokuji/internal/flash"
	"github.com/odin-software/mokuji/internal/ratelimit"
	"github.com/odin-software/mokuji/internal/session"
)

func TestIsEnabledDefaultsFalse(t *testing.T) {
	if IsEnabled() {
		t.Errorf("IsEnabled() = true, want false al inicio del proceso")
	}
}

func TestSetEnabledTogglesFlag(t *testing.T) {
	t.Cleanup(func() { SetEnabled(false) })
	SetEnabled(true)
	if !IsEnabled() {
		t.Errorf("después de SetEnabled(true), IsEnabled() debe ser true")
	}
	SetEnabled(false)
	if IsEnabled() {
		t.Errorf("después de SetEnabled(false), IsEnabled() debe ser false")
	}
}

//go:embed testdata/templates/*.html
var testTemplatesFS embed.FS

func testTemplates(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(testTemplatesFS, "testdata/templates")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	return sub
}

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	h, err := New(Config{
		Templates: testTemplates(t),
		Email:     &email.Client{},
		EmailTo:   "kg@odin.do",
		Limiter:   ratelimit.New(5, 5),
		Flasher:   flash.New(false),
		UserEmail: func(ctx context.Context, userID int64) (string, error) {
			return "user@example.com", nil
		},
	})
	if err != nil {
		t.Fatalf("feedback.New: %v", err)
	}
	return h
}

func withSession(req *http.Request, userID int64) *http.Request {
	s := &session.Session{UserID: userID}
	return req.WithContext(session.NewContext(req.Context(), s))
}

func TestGetFeedbackRendersForm(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	h.Mount(mux)

	req := httptest.NewRequest(http.MethodGet, "/feedback", nil)
	req = withSession(req, 1)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, needle := range []string{
		`name="tipo"`,
		`name="mensaje"`,
		`bug`,
		`feature`,
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("form body missing %q", needle)
		}
	}
}

// captureSender intercepta SendMail y guarda el último mensaje enviado.
type captureSender struct {
	to      []string
	from    string
	msg     []byte
	called  bool
	failErr error
}

func (c *captureSender) sendMail(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
	c.called = true
	c.to = to
	c.from = from
	c.msg = msg
	return c.failErr
}

func newEmailClient(t *testing.T, sender *captureSender) *email.Client {
	t.Helper()
	c, err := email.New(email.Config{
		Host:     "smtp.example.com",
		Port:     587,
		Username: "u",
		Password: "p",
		From:     "mokuji <auth@mokuji.local>",
	})
	if err != nil {
		t.Fatalf("email.New: %v", err)
	}
	c.SetSendMailForTesting(sender.sendMail)
	return c
}

func TestPostFeedbackSendsEmail(t *testing.T) {
	sender := &captureSender{}
	h, err := New(Config{
		Templates: testTemplates(t),
		Email:     newEmailClient(t, sender),
		EmailTo:   "kg@odin.do",
		Limiter:   ratelimit.New(5, 5),
		Flasher:   flash.New(false),
		UserEmail: func(ctx context.Context, userID int64) (string, error) {
			return "kendry@example.com", nil
		},
	})
	if err != nil {
		t.Fatalf("feedback.New: %v", err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)

	form := url.Values{}
	form.Set("tipo", "bug")
	form.Set("mensaje", "no me carga la biblioteca después del login")

	req := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withSession(req, 42)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d, want 303. body: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("location: got %q, want /", loc)
	}
	if !sender.called {
		t.Fatal("sender.called = false; expected email.SendMessage to have run")
	}
	if len(sender.to) != 1 || sender.to[0] != "kg@odin.do" {
		t.Errorf("to: got %v, want [kg@odin.do]", sender.to)
	}
	body := string(sender.msg)
	for _, needle := range []string{
		"Subject: [mokuji feedback] bug",
		"Reply-To: kendry@example.com",
		"De: kendry@example.com (user_id: 42)",
		"no me carga la biblioteca",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("email body missing %q.\nfull body:\n%s", needle, body)
		}
	}
}

func TestPostFeedbackValidatesType(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	h.Mount(mux)

	form := url.Values{}
	form.Set("tipo", "") // vacío
	form.Set("mensaje", "algo válido aquí")

	req := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withSession(req, 1)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tipo") {
		t.Errorf("body missing 'tipo' in error message")
	}
}

func TestPostFeedbackValidatesMessage(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	h.Mount(mux)

	form := url.Values{}
	form.Set("tipo", "bug")
	form.Set("mensaje", "ab") // < 4 chars

	req := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withSession(req, 1)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestPostFeedbackRateLimits(t *testing.T) {
	sender := &captureSender{}
	h, err := New(Config{
		Templates: testTemplates(t),
		Email:     newEmailClient(t, sender),
		EmailTo:   "kg@odin.do",
		// Burst 1, refill muy lento — la segunda request del mismo user
		// debe ser bloqueada.
		Limiter: ratelimit.New(rate.Every(time.Hour), 1),
		Flasher: flash.New(false),
		UserEmail: func(ctx context.Context, userID int64) (string, error) {
			return "user@example.com", nil
		},
	})
	if err != nil {
		t.Fatalf("feedback.New: %v", err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)

	doPost := func() *httptest.ResponseRecorder {
		form := url.Values{}
		form.Set("tipo", "bug")
		form.Set("mensaje", "primer mensaje del user")
		req := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = withSession(req, 7)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	first := doPost()
	if first.Code != http.StatusSeeOther {
		t.Fatalf("primer POST: got %d, want 303", first.Code)
	}

	second := doPost()
	if second.Code != http.StatusTooManyRequests {
		t.Errorf("segundo POST: got %d, want 429", second.Code)
	}
}
