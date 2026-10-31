package csrf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/odin-software/mokuji/internal/session"
)

func TestSafeMethodPassesThrough(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			called := false
			h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
			}))

			req := httptest.NewRequest(method, "/", nil)
			h.ServeHTTP(httptest.NewRecorder(), req)
			if !called {
				t.Errorf("%s: handler no fue llamado", method)
			}
		})
	}
}

func TestAnonymousPostPasses(t *testing.T) {
	// Endpoints anónimos (ej /auth/start) no requieren CSRF (ADR 0006).
	called := false
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/auth/start", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Error("POST anónimo debería pasar sin CSRF")
	}
}

func TestAuthenticatedPostWithValidFormToken(t *testing.T) {
	called := false
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	body := url.Values{"csrf_token": {"valid-token"}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(session.NewContext(context.Background(),
		&session.Session{CSRFToken: "valid-token"}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: esperado 200, obtenido %d", rec.Code)
	}
	if !called {
		t.Error("handler no fue llamado")
	}
}

func TestAuthenticatedPostWithValidHeaderToken(t *testing.T) {
	called := false
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-CSRF-Token", "valid-token")
	req = req.WithContext(session.NewContext(context.Background(),
		&session.Session{CSRFToken: "valid-token"}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: esperado 200, obtenido %d", rec.Code)
	}
	if !called {
		t.Error("handler no fue llamado")
	}
}

func TestAuthenticatedPostWithBadToken(t *testing.T) {
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler no debería ser llamado")
	}))

	body := url.Values{"csrf_token": {"wrong-token"}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(session.NewContext(context.Background(),
		&session.Session{CSRFToken: "right-token"}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status: esperado 403, obtenido %d", rec.Code)
	}
}

func TestAuthenticatedPostWithMissingToken(t *testing.T) {
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler no debería ser llamado")
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(session.NewContext(context.Background(),
		&session.Session{CSRFToken: "right-token"}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status: esperado 403, obtenido %d", rec.Code)
	}
}

func TestInput(t *testing.T) {
	out := string(Input(&session.Session{CSRFToken: "tok123"}))
	want := `<input type="hidden" name="csrf_token" value="tok123">`
	if out != want {
		t.Errorf("Input output:\nesperado %q\nobtenido %q", want, out)
	}
}

func TestInputNilSession(t *testing.T) {
	if out := Input(nil); out != "" {
		t.Errorf("Input(nil) debería ser vacío, obtenido %q", out)
	}
}

func TestInputEscapesValue(t *testing.T) {
	// Si por algún bug el token contiene caracteres HTML, deben ser escapados
	// para evitar XSS al inyectar en una plantilla.
	out := string(Input(&session.Session{CSRFToken: `"><script>alert(1)</script>`}))
	if strings.Contains(out, "<script>") {
		t.Errorf("Input no escapó tags HTML: %q", out)
	}
	if strings.Contains(out, `"><`) {
		t.Errorf("Input no escapó comillas: %q", out)
	}
}
