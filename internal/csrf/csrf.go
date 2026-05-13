// Package csrf implementa el middleware CSRF descrito en ADR 0006.
//
// Modelo: synchronizer token atado a la sesión. El token vive en la fila de
// la sesión (csrf_token) y se valida contra el form field csrf_token o el
// header X-CSRF-Token. Solo aplica a requests autenticados con métodos
// state-changing (POST/PUT/DELETE/PATCH); requests anónimos pasan sin
// validación porque no tienen credenciales que un atacante pueda abusar
// (ADR 0006).
package csrf

import (
	"bytes"
	"crypto/subtle"
	"html/template"
	"net/http"

	"git.odin.do/kengru/mokuji/internal/session"
)

const (
	// FormField es el nombre del campo hidden que contiene el token en forms.
	FormField = "csrf_token"
	// HeaderName es el header alternativo para clientes que mandan JSON o
	// quieren enviar el token sin estar en un form.
	HeaderName = "X-CSRF-Token"
)

// Middleware valida el token CSRF en requests state-changing autenticados.
// Requests con método safe (GET/HEAD/OPTIONS) o sin sesión pasan sin
// validación.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		s, ok := session.FromContext(r.Context())
		if !ok {
			// Sin sesión no hay credenciales que abusar (ADR 0006).
			next.ServeHTTP(w, r)
			return
		}

		token := r.Header.Get(HeaderName)
		if token == "" {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			token = r.PostFormValue(FormField)
		}

		if !valid(s.CSRFToken, token) {
			http.Error(w, "csrf token mismatch", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func valid(want, got string) bool {
	if want == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

var inputTmpl = template.Must(template.New("csrf").Parse(
	`<input type="hidden" name="csrf_token" value="{{.}}">`,
))

// Input retorna un <input type="hidden"> con el token CSRF de la sesión,
// listo para inyectar en forms desde plantillas:
//
//	{{ csrfInput .Session }}
//
// Si Session es nil, retorna cadena vacía. El valor se escapa con
// html/template para defensa en profundidad (los tokens en sí son base64url
// y no requieren escape, pero la lógica no asume esa garantía).
func Input(s *session.Session) template.HTML {
	if s == nil {
		return ""
	}
	var buf bytes.Buffer
	_ = inputTmpl.Execute(&buf, s.CSRFToken)
	return template.HTML(buf.String())
}
