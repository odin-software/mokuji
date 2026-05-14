package server

import "net/http"

// handleHealth responde 200 con un body trivial. Sirve para que load balancers
// y monitoreo verifiquen que el proceso está vivo.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// serveManifest sirve el manifest.webmanifest con el Content-Type correcto
// (que mime.TypeByExtension no siempre tiene registrado para .webmanifest).
func (s *Server) serveManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	http.ServeFileFS(w, r, s.cfg.StaticAssets, "manifest.webmanifest")
}

// serveSW sirve el service worker con dos headers críticos:
//   - Service-Worker-Allowed: / permite registrar el SW con scope / aunque
//     viva en /sw.js (sin este header el browser limita el scope al path
//     donde vive el archivo).
//   - Cache-Control: no-cache evita que los browsers cacheen el SW por hasta
//     24 horas y demoren la propagación de actualizaciones.
func (s *Server) serveSW(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Service-Worker-Allowed", "/")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.cfg.StaticAssets, "sw.js")
}
