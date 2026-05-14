package server

import (
	"io/fs"
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
	if s.cfg.Home != nil {
		s.cfg.Home.Mount(mux)
	}
	if s.cfg.Auth != nil {
		s.cfg.Auth.Mount(mux)
	}
	if s.cfg.StaticAssets != nil {
		s.mountPWA(mux)
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

// mountPWA registra los endpoints necesarios para que mokuji sea instalable
// como PWA. /sw.js y /manifest.webmanifest se sirven en el root para que el
// scope del service worker sea / por default; /static/ sirve los demás
// assets (íconos, eventualmente CSS).
func (s *Server) mountPWA(mux *http.ServeMux) {
	staticSub, err := fs.Sub(s.cfg.StaticAssets, "static")
	if err == nil {
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))
	}
	mux.HandleFunc("GET /manifest.webmanifest", s.serveManifest)
	mux.HandleFunc("GET /sw.js", s.serveSW)
}
