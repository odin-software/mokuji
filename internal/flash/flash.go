// Package flash implementa mensajes "flash" one-shot — feedback efímero
// que sobrevive un redirect (POST → 303 → GET) y se consume en el siguiente
// render.
//
// Storage: cookie HTTP no firmada con TTL de 30s. Stateless, zero DB,
// zero cleanup. El contenido no es sensible (típicamente "cambios
// guardados" o "error en X"), así que self-tampering del usuario sobre
// su propio mensaje no afecta a nadie más — solo se engaña a sí mismo.
// Si en el futuro hay que mostrar mensajes con autoridad (ej. "tu pago
// se procesó por $X"), migrar a cookie firmada con HMAC o a tabla
// server-side; mientras tanto este patrón cubre el 100% de los casos.
//
// Uso típico:
//
//	// Handler que recibe POST:
//	flasher.Set(w, flash.KindSuccess, "cambios guardados")
//	http.Redirect(w, r, "/settings", http.StatusSeeOther)
//
//	// Handler que renderiza GET:
//	kind, msg, ok := flasher.Pop(w, r) // borra la cookie atómicamente
//	if ok {
//	    data.Flash = &flash.Flash{Kind: kind, Message: msg}
//	}
package flash

import (
	"net/http"
	"net/url"
	"strings"
)

// Constantes públicas para el "kind" del mensaje. El template decide
// cómo renderearlas (color, ícono). Si agregas un kind nuevo, actualiza
// los templates que renderean flashes.
const (
	KindSuccess = "success"
	KindError   = "error"
	KindInfo    = "info"
)

// cookieName es el nombre de la cookie. Underscore prefix para señalar
// "interno de la app", no tocable por extensiones legítimas.
const cookieName = "_mokuji_flash"

// maxAge limita cuánto vive la cookie si el GET subsiguiente nunca
// ocurre (ej. user cierra el browser tras el POST). 30s da margen para
// el redirect inmediato sin dejar mensajes obsoletos atrás.
const maxAge = 30

// Flash es el par (kind, message) listo para pasar al template.
type Flash struct {
	Kind    string
	Message string
}

// Flasher genera y consume cookies de flash. Construir con New.
// Stateless — una instancia compartida es segura entre handlers.
type Flasher struct {
	secure bool // habilitar flag Secure de la cookie (HTTPS)
}

// New construye un Flasher. `secure` debe ser true en producción
// (HTTPS); en dev local sin TLS, false.
func New(secure bool) *Flasher {
	return &Flasher{secure: secure}
}

// Set graba el flash en una cookie. Llamar antes del Redirect — el
// browser persiste la cookie y la manda en el GET subsiguiente.
//
// Si message contiene caracteres no-ASCII o reservados, se url-encode
// dentro del cookie value (los browsers son estrictos con esos chars).
func (f *Flasher) Set(w http.ResponseWriter, kind, message string) {
	// Formato: "kind|url-encoded-message". Pipe como separador es
	// seguro porque url.QueryEscape no produce "|".
	value := kind + "|" + url.QueryEscape(message)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   f.secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// Pop lee el flash de la request y borra la cookie atómicamente
// (escribe un Set-Cookie con MaxAge=-1 antes de devolver). Si no hay
// cookie o está malformada, devuelve ok=false sin error.
//
// Borrar siempre — incluso si el contenido es inválido — evita que
// una cookie corrupta se quede pegada para siempre.
func (f *Flasher) Pop(w http.ResponseWriter, r *http.Request) (kind, message string, ok bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", "", false
	}

	// Borrado one-shot. Se escribe ANTES del parse para que una cookie
	// malformada también se limpie.
	f.clear(w)

	parts := strings.SplitN(c.Value, "|", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	msg, err := url.QueryUnescape(parts[1])
	if err != nil {
		return "", "", false
	}
	if parts[0] == "" {
		return "", "", false
	}
	return parts[0], msg, true
}

func (f *Flasher) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   f.secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
