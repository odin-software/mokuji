// Package httpx agrupa helpers de HTTP compartidos entre handlers.
package httpx

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP identifica al cliente de r, para rate limiting y logs.
//
// trustProxy=true hace que lea X-Forwarded-For y tome la primera entrada —
// el cliente original; el resto de la cadena son proxies intermedios. Solo
// es seguro detrás de un reverse proxy de confianza: cualquiera puede mandar
// ese header, así que leerlo sin un proxy que lo sobreescriba permite evadir
// cualquier rate limit rotando el valor.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.Index(xff, ","); i != -1 {
				xff = xff[:i]
			}
			return strings.TrimSpace(xff)
		}
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
