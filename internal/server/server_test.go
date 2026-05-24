package server

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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
