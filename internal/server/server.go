// Package server contiene el servidor HTTP de mokuji, su composición de
// middleware, y los handlers de cada feature.
//
// Las dependencias del servidor (DB, logger, sessions, auth handler) se
// pasan vía Config para que el servidor sea testeable sin globals.
package server

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"git.odin.do/kengru/mokuji/internal/admin"
	"git.odin.do/kengru/mokuji/internal/auth"
	"git.odin.do/kengru/mokuji/internal/feedback"
	"git.odin.do/kengru/mokuji/internal/home"
	"git.odin.do/kengru/mokuji/internal/library"
	"git.odin.do/kengru/mokuji/internal/onboarding"
	"git.odin.do/kengru/mokuji/internal/profile"
	releaseshandler "git.odin.do/kengru/mokuji/internal/releases/handler"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/unsubscribe"
)

// Config agrupa las dependencias del servidor. Todos los Handlers son
// opcionales para facilitar tests del transporte HTTP sin un stack completo.
type Config struct {
	Addr string
	// BaseURL es el origen público del servidor (ej. "https://mokuji.org").
	// Usado por handlers que emiten URLs absolutas (sitemap, robots).
	BaseURL     string
	DB          *sql.DB
	Logger      *slog.Logger
	Sessions    *session.Manager
	Auth        *auth.Handler
	Home        *home.Handler
	Library     *library.Handler
	Profile     *profile.Handler
	Releases    *releaseshandler.Handler
	Onboarding  *onboarding.Handler
	Unsubscribe *unsubscribe.Handler
	Admin       *admin.Handler
	Feedback    *feedback.Handler
	// StaticAssets es el FS de la carpeta web/ del repo (contiene static/,
	// manifest.webmanifest, sw.js). Si es nil, los endpoints PWA no se
	// montan.
	StaticAssets fs.FS
	// HTMLTemplates es el FS de web/templates/. Si es nil, las páginas 404
	// y 500 caen al fallback plano de net/http. En prod siempre se pasa.
	HTMLTemplates fs.FS
}

// Server envuelve un *http.Server con su composición de routing y middleware.
type Server struct {
	cfg  Config
	http *http.Server
	// errs renderiza 404 y 500. Es nil si HTTPTemplates no fue provisto
	// (tests sin assets); en ese caso se cae al fallback plano.
	errs *errorRenderer
}

// New construye un servidor listo para Start.
func New(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	s := &Server{cfg: cfg}

	if cfg.HTMLTemplates != nil {
		errs, err := newErrorRenderer(cfg.HTMLTemplates, cfg.Logger)
		if err != nil {
			cfg.Logger.Error("inicializar error renderer", "err", err)
			// Seguimos arrancando sin páginas estilizadas: mejor que tumbar
			// el servidor entero por un template roto.
		} else {
			s.errs = errs
		}
	}

	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s
}

// Start bloquea sirviendo requests hasta que Shutdown sea llamado o el
// servidor falle. Retorna nil en shutdown limpio.
func (s *Server) Start() error {
	s.cfg.Logger.Info("servidor arrancando", "addr", s.cfg.Addr)
	if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown apaga el servidor con timeout, permitiendo que requests en curso
// terminen antes de cerrar.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
