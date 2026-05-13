package session

import (
	"context"
	"net/http"
)

// sessionKey es la clave bajo la cual la Session vive en el context.Context.
type sessionKey struct{}

// Middleware carga la sesión (si hay cookie válida) y la adjunta al context.
// Errores de DB se loguean; el request continúa como anónimo en vez de
// romperse — un fallo de DB no debería tumbar requests públicas.
//
// Si había cookie pero la sesión no se pudo cargar (revocada o expirada), se
// limpia la cookie en la response para evitar que el cliente la siga mandando.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := m.Load(r)
		if err != nil {
			m.logger.Error("cargando sesión",
				"err", err,
				"path", r.URL.Path,
			)
		}

		if s != nil {
			r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, s))
		} else if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
			m.clearCookie(w)
		}

		next.ServeHTTP(w, r)
	})
}

// FromContext extrae la Session del context. Retorna (nil, false) si no hay
// sesión cargada.
func FromContext(ctx context.Context) (*Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(*Session)
	return s, ok
}

// NewContext retorna un context con la sesión adjuntada. Útil para tests y
// para casos donde se construye un request autenticado sin pasar por la
// cookie y el Middleware.
func NewContext(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}
