package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIPSinProxyIgnoraXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := ClientIP(req, false); got != "203.0.113.9" {
		t.Errorf("con trustProxy=false esperaba 203.0.113.9, got %q", got)
	}
}

func TestClientIPConProxyLeeXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := ClientIP(req, true); got != "1.2.3.4" {
		t.Errorf("esperaba 1.2.3.4, got %q", got)
	}
}

func TestClientIPTomaElPrimeroDeLaCadena(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.2, 10.0.0.3")

	if got := ClientIP(req, true); got != "1.2.3.4" {
		t.Errorf("esperaba el primer IP de la cadena, got %q", got)
	}
}

func TestClientIPCaeARemoteAddrSinXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:54321"

	if got := ClientIP(req, true); got != "203.0.113.9" {
		t.Errorf("sin XFF esperaba RemoteAddr, got %q", got)
	}
}

func TestClientIPRemoteAddrSinPuerto(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9"

	if got := ClientIP(req, false); got != "203.0.113.9" {
		t.Errorf("RemoteAddr sin puerto debía pasar tal cual, got %q", got)
	}
}
