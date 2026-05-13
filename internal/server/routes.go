package server

import (
	"net/http"

	"git.odin.do/kengru/mokuji/internal/csrf"
)

// routes monta los endpoints y aplica la cadena de middleware.
//
// Orden de la cadena (de afuera hacia adentro):
//
//	recoverer -> requestID -> logRequests -> session -> csrf -> mux
//
// recoverer va más afuera para cubrir panics en cualquier middleware.
// requestID va antes que logRequests para que el logger incluya el ID.
// session y csrf solo se aplican si cfg.Sessions está configurado; permite
// instanciar el servidor en tests sin stack completo.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	if s.cfg.Auth != nil {
		s.cfg.Auth.Mount(mux)
	}

	var h http.Handler = mux
	if s.cfg.Sessions != nil {
		h = csrf.Middleware(h)
		h = s.cfg.Sessions.Middleware(h)
	}
	h = s.logRequests(h)
	h = s.requestID(h)
	h = s.recoverer(h)
	return h
}
