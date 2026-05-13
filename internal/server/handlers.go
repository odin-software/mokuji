package server

import "net/http"

// handleHealth responde 200 con un body trivial. Sirve para que load balancers
// y monitoreo verifiquen que el proceso está vivo.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
