package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"
)

// requestIDKey es la clave (tipo único, no exportada) bajo la cual se guarda
// el request ID en el context.Context de cada request.
type requestIDKey struct{}

// RequestIDFromContext extrae el request ID del context. Retorna string vacío
// si no fue seteado (ej: requests construidas en tests sin el middleware).
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// recoverer captura panics en handlers y los reporta como 500, evitando que
// tumben el servidor. Debe estar lo más afuera posible en la cadena para
// también cubrir panics dentro de otros middleware.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.cfg.Logger.Error("panic recuperado",
					"panic", rec,
					"path", r.URL.Path,
					"method", r.Method,
					"request_id", RequestIDFromContext(r.Context()),
				)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestID asigna un ID aleatorio a cada request, lo expone en el header
// X-Request-ID de la respuesta y lo guarda en el context para que otros
// componentes lo loguen.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// responseRecorder envuelve un http.ResponseWriter para capturar el status
// code y bytes escritos, datos que el logger emite al cerrar la request.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.status = code
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	if rr.status == 0 {
		rr.status = http.StatusOK
	}
	n, err := rr.ResponseWriter.Write(b)
	rr.bytes += int64(n)
	return n, err
}

// contentSecurityPolicy es la directiva CSP que mokuji aplica a todas las
// respuestas. Notas:
//   - script-src 'self': solo scripts desde nuestro propio origen. Los
//     inline scripts ya no se permiten (extraímos el registro del SW a
//     /static/app.js para que pase).
//   - style-src 'self' 'unsafe-inline': layout.html todavía tiene un <style>
//     embedido; cuando se extraiga ese CSS a /static/app.css se puede
//     remover 'unsafe-inline'.
//   - img-src 'self' https: data:: covers de AniList vienen de CDNs HTTPS
//     que cambian de hostname; permitir 'https:' es pragmático.
//   - frame-ancestors 'none': equivalente moderno de X-Frame-Options DENY.
//   - form-action 'self': los forms solo postean a mokuji.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' https: data:; " +
	"connect-src 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"object-src 'none'"

// securityHeaders setea headers de seguridad estándar en todas las respuestas.
//
// Headers incluidos:
//   - Strict-Transport-Security: fuerza HTTPS en visitas futuras del navegador.
//     Se incluye con max-age de 180 días + includeSubDomains. Sin 'preload'
//     porque preload es irreversible y prematuro.
//   - X-Frame-Options DENY: bloquea embedding de mokuji en iframes
//     (defensa contra clickjacking).
//   - X-Content-Type-Options nosniff: evita que el browser adivine MIME types.
//   - Referrer-Policy strict-origin-when-cross-origin: comparte origin
//     cross-site, full URL same-site, nada en downgrade HTTP/HTTPS.
//   - Content-Security-Policy: ver contentSecurityPolicy arriba.
//   - Permissions-Policy: bloquea explícitamente APIs sensibles que no usamos.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=15552000; includeSubDomains")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		next.ServeHTTP(w, r)
	})
}

// logRequests emite una línea de log por request con método, path, status,
// bytes y duración.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		s.cfg.Logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", RequestIDFromContext(r.Context()),
		)
	})
}
