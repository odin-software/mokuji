package csrf

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.odin.do/kengru/mokuji/internal/session"
)

// multipartReq arma un POST multipart autenticado con un campo csrf_token
// (si token != "") y un archivo "file" con el contenido dado.
func multipartReq(t *testing.T, token string, file []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if token != "" {
		if err := mw.WriteField(FormField, token); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile("file", "mangalist.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(file); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/settings/import/mal", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req.WithContext(session.NewContext(context.Background(),
		&session.Session{CSRFToken: "valid-token"}))
}

func TestAuthenticatedMultipartWithValidToken(t *testing.T) {
	var got []byte
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("FormFile: %v", err)
			return
		}
		defer f.Close()
		got, _ = io.ReadAll(f)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, multipartReq(t, "valid-token", []byte("<myanimelist/>")))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", rec.Code)
	}
	if string(got) != "<myanimelist/>" {
		t.Errorf("archivo: esperado <myanimelist/>, obtenido %q", got)
	}
}

func TestAuthenticatedMultipartWithoutToken(t *testing.T) {
	called := false
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, multipartReq(t, "", []byte("x")))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status: esperado 403, obtenido %d", rec.Code)
	}
	if called {
		t.Error("handler no debería ser llamado sin token")
	}
}

func TestAuthenticatedMultipartTooLarge(t *testing.T) {
	called := false
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, multipartReq(t, "valid-token", make([]byte, MaxMultipartBytes+1)))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status: esperado 413, obtenido %d", rec.Code)
	}
	if called {
		t.Error("handler no debería ser llamado con body excedido")
	}
}
