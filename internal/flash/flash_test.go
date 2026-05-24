package flash

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetAndPopRoundtrip(t *testing.T) {
	f := New(false)

	// Set graba la cookie.
	rec := httptest.NewRecorder()
	f.Set(rec, KindSuccess, "cambios guardados")

	cookieValue := extractCookie(t, rec, cookieName)
	if cookieValue == "" {
		t.Fatal("Set no escribió cookie")
	}

	// Pop la lee desde una request que la carga.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookieValue})
	popRec := httptest.NewRecorder()

	kind, msg, ok := f.Pop(popRec, req)
	if !ok {
		t.Fatal("Pop ok=false")
	}
	if kind != KindSuccess {
		t.Errorf("kind: esperado %q, obtuve %q", KindSuccess, kind)
	}
	if msg != "cambios guardados" {
		t.Errorf("message: esperado 'cambios guardados', obtuve %q", msg)
	}

	// Pop debe haber borrado la cookie via Set-Cookie con MaxAge<=0.
	deleted := extractCookieHeader(t, popRec, cookieName)
	if !strings.Contains(deleted, "Max-Age=0") && !strings.Contains(deleted, "Max-Age=-1") {
		t.Errorf("Pop no escribió Set-Cookie de borrado: %s", deleted)
	}
}

func TestPopWithoutCookieReturnsNotOK(t *testing.T) {
	f := New(false)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	_, _, ok := f.Pop(rec, req)
	if ok {
		t.Error("Pop con cookie ausente devolvió ok=true")
	}
}

func TestPopMalformedCookieClearsAndReturnsNotOK(t *testing.T) {
	// Una cookie sin el separador "|" es inválida. Pop debe limpiar y
	// devolver ok=false para que la siguiente request no reintente
	// parsearla.
	f := New(false)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "garbage-no-pipe"})
	rec := httptest.NewRecorder()

	_, _, ok := f.Pop(rec, req)
	if ok {
		t.Error("Pop con cookie malformada devolvió ok=true")
	}
	// Verificar que se limpió.
	deleted := extractCookieHeader(t, rec, cookieName)
	if !strings.Contains(deleted, "Max-Age=0") && !strings.Contains(deleted, "Max-Age=-1") {
		t.Errorf("cookie malformada no fue limpiada: %s", deleted)
	}
}

func TestSetHandlesNonASCIIMessage(t *testing.T) {
	// Mensajes con tildes/eñe deben roundtrip íntegros.
	f := New(false)
	rec := httptest.NewRecorder()
	f.Set(rec, KindError, "validación: número inválido, año <2020>")

	cookieValue := extractCookie(t, rec, cookieName)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookieValue})
	popRec := httptest.NewRecorder()

	_, msg, ok := f.Pop(popRec, req)
	if !ok {
		t.Fatal("Pop ok=false con mensaje unicode")
	}
	if msg != "validación: número inválido, año <2020>" {
		t.Errorf("roundtrip unicode falló: %q", msg)
	}
}

func TestSetSecureFlagHonored(t *testing.T) {
	// Con secure=true, la cookie debe incluir el flag Secure.
	f := New(true)
	rec := httptest.NewRecorder()
	f.Set(rec, KindInfo, "hola")

	h := extractCookieHeader(t, rec, cookieName)
	if !strings.Contains(h, "Secure") {
		t.Errorf("Secure flag faltante con secure=true: %s", h)
	}
}

// extractCookie devuelve solo el value de la cookie con name `name`.
func extractCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// extractCookieHeader devuelve el Set-Cookie completo (con atributos)
// para chequear MaxAge, Secure, etc.
func extractCookieHeader(t *testing.T, rec *httptest.ResponseRecorder, name string) string {
	t.Helper()
	for _, h := range rec.Result().Header["Set-Cookie"] {
		if strings.HasPrefix(h, name+"=") {
			return h
		}
	}
	return ""
}
