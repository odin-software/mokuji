// Package home sirve la página raíz de la app.
//
// Si el usuario tiene sesión válida, muestra su email y un botón de logout.
// Si no, redirige a /auth.
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
	"git.odin.do/kengru/mokuji/internal/session"
)

// Config agrupa las dependencias del handler.
type Config struct {
	DB            *sql.DB
	HTMLTemplates fs.FS // contiene layout.html y home.html
	Logger        *slog.Logger
}

// Handler sirve GET /.
type Handler struct {
	db     *sql.DB
	tmpl   *template.Template
	logger *slog.Logger
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
		"csrfInput": csrf.Input,
	}
	tmpl, err := template.New("base").Funcs(funcs).ParseFS(
		cfg.HTMLTemplates, "layout.html", "home.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parsear templates: %w", err)
	}

	return &Handler{db: cfg.DB, tmpl: tmpl, logger: cfg.Logger}, nil
}

// Mount registra el endpoint en el mux. Usa `{$}` para matchear solo el path
// exacto "/" en vez de ser catch-all.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", h.home)
}

type homeData struct {
	Email   string
	Session *session.Session
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}

	var emailAddr string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT email FROM users WHERE id = ?`, s.UserID,
	).Scan(&emailAddr)
	if err != nil {
		h.logger.Error("cargar email de usuario", "user_id", s.UserID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "layout", homeData{
		Email:   emailAddr,
		Session: s,
	}); err != nil {
		h.logger.Error("renderizar home", "err", err)
	}
}
