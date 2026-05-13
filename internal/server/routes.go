package server

import "net/http"

// routes monta los endpoints y aplica la cadena de middleware.
//
// Orden de la cadena (de afuera hacia adentro):
//
//	recoverer -> requestID -> logRequests -> mux
//
// recoverer va más afuera para cubrir panics en cualquier middleware. requestID
// va antes que logRequests para que el logger pueda incluir el ID.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)

	var h http.Handler = mux
	h = s.logRequests(h)
	h = s.requestID(h)
	h = s.recoverer(h)
	return h
}
