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
