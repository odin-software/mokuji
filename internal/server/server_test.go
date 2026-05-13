package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
