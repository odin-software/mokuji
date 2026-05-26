package feedback

import (
	"embed"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/flash"
	"git.odin.do/kengru/mokuji/internal/ratelimit"
	"git.odin.do/kengru/mokuji/internal/session"
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
