package turnstile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeSiteverify(t *testing.T, success bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsear form: %v", err)
		}
		if r.FormValue("secret") == "" {
			t.Error("la request no mandó secret")
		}
		if r.FormValue("response") == "" {
			t.Error("la request no mandó response")
		}
		w.Header().Set("Content-Type", "application/json")
		if success {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVerifyAceptaTokenValido(t *testing.T) {
	srv := fakeSiteverify(t, true)
	v := New("secreto")
	v.SetEndpointForTesting(srv.URL)

	ok, err := v.Verify(context.Background(), "token-del-widget", "1.2.3.4")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("token válido fue rechazado")
	}
}

func TestVerifyRechazaTokenInvalido(t *testing.T) {
	srv := fakeSiteverify(t, false)
	v := New("secreto")
	v.SetEndpointForTesting(srv.URL)

	ok, err := v.Verify(context.Background(), "basura", "1.2.3.4")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Error("token inválido fue aceptado")
	}
}

func TestVerifyRechazaTokenVacioSinLlamarAlAPI(t *testing.T) {
	llamado := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llamado = true
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	t.Cleanup(srv.Close)

	v := New("secreto")
	v.SetEndpointForTesting(srv.URL)

	ok, err := v.Verify(context.Background(), "", "1.2.3.4")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Error("token vacío fue aceptado")
	}
	if llamado {
		t.Error("se llamó al API con token vacío; debía cortocircuitar")
	}
}

func TestVerifierNilPermiteTodo(t *testing.T) {
	var v *Verifier

	ok, err := v.Verify(context.Background(), "", "")
	if err != nil {
		t.Fatalf("Verify sobre nil: %v", err)
	}
	if !ok {
		t.Error("un Verifier nil debe permitir todo (dev sin keys)")
	}
}

func TestVerifyPropagaErrorDeRed(t *testing.T) {
	v := New("secreto")
	// Puerto cerrado: la llamada tiene que fallar, no retornar false en
	// silencio. El caller decide qué hacer con un Cloudflare caído.
	v.SetEndpointForTesting("http://127.0.0.1:1")

	if _, err := v.Verify(context.Background(), "token", "1.2.3.4"); err == nil {
		t.Error("esperaba error de red, got nil")
	}
}

// TestVerifyFallaSiSiteverifyRespondeNo200 cubre un Cloudflare que responde
// (sin error de transporte) pero con un status distinto de 200 — un 5xx de
// brownout, o un balanceador devolviendo una página de error. Es una rama
// fail-closed sin test previo: si alguien la reescribiera para ignorar el
// status y decodificar igual, un 503 de Cloudflare con body vacío pasaría
// silenciosamente como (false, nil) en vez de propagar el error, y el
// caller (start) no tendría forma de distinguir "reto no resuelto" de
// "Cloudflare no puede responder".
func TestVerifyFallaSiSiteverifyRespondeNo200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	v := New("secreto")
	v.SetEndpointForTesting(srv.URL)

	ok, err := v.Verify(context.Background(), "token", "1.2.3.4")
	if err == nil {
		t.Error("esperaba error por status no-200, got nil")
	}
	if ok {
		t.Error("un status no-200 no puede resultar en verificación exitosa")
	}
}

// TestVerifyFallaSiSiteverifyRespondeJSONMalformado cubre un 200 con body
// que no es el JSON esperado — por ejemplo la página HTML de error de un
// proxy delante de Cloudflare. Misma razón que el test de arriba: es la otra
// rama fail-closed sin cobertura, y silenciarla en vez de propagar el error
// convertiría un problema de infraestructura en un rechazo silencioso
// indistinguible de un reto real fallido.
func TestVerifyFallaSiSiteverifyRespondeJSONMalformado(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	t.Cleanup(srv.Close)

	v := New("secreto")
	v.SetEndpointForTesting(srv.URL)

	ok, err := v.Verify(context.Background(), "token", "1.2.3.4")
	if err == nil {
		t.Error("esperaba error por JSON malformado, got nil")
	}
	if ok {
		t.Error("un body malformado no puede resultar en verificación exitosa")
	}
}
