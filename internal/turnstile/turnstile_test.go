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
