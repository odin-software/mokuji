// Package home sirve la página raíz de la app.
//
// Si el usuario tiene sesión válida, lo manda a su biblioteca (D4: no hay
// dashboard; la biblioteca ordenada es la entrada). Si no, sirve la
// landing pública.
package home

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"git.odin.do/kengru/mokuji/internal/csrf"
	"git.odin.do/kengru/mokuji/internal/errrender"
	"git.odin.do/kengru/mokuji/internal/feedback"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/version"
)

// Config agrupa las dependencias del handler.
type Config struct {
	DB            *sql.DB
	HTMLTemplates fs.FS // contiene layout.html, landing.html y privacy.html
	Logger        *slog.Logger
	Errs          *errrender.ErrorRenderer // nil en tests sin assets
}

// Handler sirve GET /.
type Handler struct {
	db          *sql.DB
	tmplLanding *template.Template
	tmplPrivacy *template.Template
	logger      *slog.Logger
	errs        *errrender.ErrorRenderer
}

// New construye un Handler. Falla si las plantillas no parsean.
func New(cfg Config) (*Handler, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	funcs := template.FuncMap{
		"csrfInput":       csrf.Input,
		"appVersion":      func() string { return version.Value },
		"feedbackEnabled": feedback.IsEnabled,
	}
	tmplLanding, err := template.New("base").Funcs(funcs).ParseFS(
		cfg.HTMLTemplates, "layout.html", "landing.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parsear templates landing: %w", err)
	}
	tmplPrivacy, err := template.New("base").Funcs(funcs).ParseFS(
		cfg.HTMLTemplates, "layout.html", "privacy.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parsear templates privacy: %w", err)
	}

	return &Handler{
		db:          cfg.DB,
		tmplLanding: tmplLanding,
		tmplPrivacy: tmplPrivacy,
		logger:      cfg.Logger,
		errs:        cfg.Errs,
	}, nil
}

// Mount registra el endpoint en el mux. Usa `{$}` para matchear solo el path
// exacto "/" en vez de ser catch-all.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", h.home)
	mux.HandleFunc("GET /privacy", h.privacy)
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	if _, ok := session.FromContext(r.Context()); ok {
		http.Redirect(w, r, "/library", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplLanding.ExecuteTemplate(w, "layout", landingData{}); err != nil {
		h.logger.Error("renderizar landing", "err", err)
	}
}

// privacy sirve GET /privacy. Página pública sin auth; el contenido refleja
// las prácticas de mokuji.org. Self-hosters definen las propias.
func (h *Handler) privacy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Renderiza con landingData{} (Session nil) para que el layout no muestre
	// sidebar/nav autenticada — la página es pública y se ve igual con o sin
	// sesión.
	if err := h.tmplPrivacy.ExecuteTemplate(w, "layout", landingData{}); err != nil {
		h.logger.Error("renderizar privacy", "err", err)
	}
}

// landingData es el payload del template landing.html para visitantes
// sin sesión. Session es nil — layout.html lo usa con {{if .Session}} para
// mostrar/ocultar la nav autenticada; el campo tiene que existir para que
// html/template no falle con "can't evaluate field Session".
type landingData struct {
	Session *session.Session
}
