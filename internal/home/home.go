// Package home sirve la página raíz de la app.
//
// Si el usuario tiene sesión válida, muestra su email y un botón de logout.
// Si no, sirve la landing pública.
package home

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"git.odin.do/kengru/mokuji/internal/csrf"
	releaseshandler "git.odin.do/kengru/mokuji/internal/releases/handler"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/version"
)

// HomeWidgetReleases es el cap de releases que muestra el widget en /.
// Mantenerlo chico — la lista completa vive en /releases.
const HomeWidgetReleases = 5

// ReleasesProvider abstrae la fuente de releases para el widget. En prod
// es *releaseshandler.Handler; en tests se mockea con cualquier impl.
type ReleasesProvider interface {
	UnreadReleases(ctx context.Context, userID int64, limit int) ([]releaseshandler.Release, error)
}

// Config agrupa las dependencias del handler.
type Config struct {
	DB            *sql.DB
	HTMLTemplates fs.FS // contiene layout.html, home.html y landing.html
	Releases      ReleasesProvider
	Logger        *slog.Logger
}

// Handler sirve GET /.
type Handler struct {
	db            *sql.DB
	tmplDashboard *template.Template
	tmplLanding   *template.Template
	releases      ReleasesProvider
	logger        *slog.Logger
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
		"csrfInput":  csrf.Input,
		"appVersion": func() string { return version.Value },
	}
	tmplDashboard, err := template.New("base").Funcs(funcs).ParseFS(
		cfg.HTMLTemplates, "layout.html", "home.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parsear templates dashboard: %w", err)
	}
	tmplLanding, err := template.New("base").Funcs(funcs).ParseFS(
		cfg.HTMLTemplates, "layout.html", "landing.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parsear templates landing: %w", err)
	}

	return &Handler{
		db:            cfg.DB,
		tmplDashboard: tmplDashboard,
		tmplLanding:   tmplLanding,
		releases:      cfg.Releases,
		logger:        cfg.Logger,
	}, nil
}

// Mount registra el endpoint en el mux. Usa `{$}` para matchear solo el path
// exacto "/" en vez de ser catch-all.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", h.home)
}

type homeData struct {
	Email    string
	Username string
	Session  *session.Session
	Releases []releaseshandler.Release
	// HasUsername=false dispara el banner que invita a setear slug. Una vez
	// elegido, el banner desaparece sin necesidad de tracking adicional.
	HasUsername bool
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := h.tmplLanding.ExecuteTemplate(w, "layout", landingData{}); err != nil {
			h.logger.Error("renderizar landing", "err", err)
		}
		return
	}

	var emailAddr string
	var slug sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT email, slug FROM users WHERE id = ?`, s.UserID,
	).Scan(&emailAddr, &slug)
	if err != nil {
		h.logger.Error("cargar email de usuario", "user_id", s.UserID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Widget de releases: best-effort. Si falla la query, logueamos y
	// renderizamos la home sin el widget — no rompe la página por algo
	// secundario. Si el provider es nil (caso edge: tests viejos), salta.
	var releases []releaseshandler.Release
	if h.releases != nil {
		releases, err = h.releases.UnreadReleases(r.Context(), s.UserID, HomeWidgetReleases)
		if err != nil {
			h.logger.Error("cargar widget releases", "user_id", s.UserID, "err", err)
			releases = nil
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplDashboard.ExecuteTemplate(w, "layout", homeData{
		Email:       emailAddr,
		Username:    slug.String,
		Session:     s,
		Releases:    releases,
		HasUsername: slug.Valid,
	}); err != nil {
		h.logger.Error("renderizar home", "err", err)
	}
}

// landingData es el payload del template landing.html para visitantes
// sin sesión. Session es nil — layout.html lo usa con {{if .Session}} para
// mostrar/ocultar la nav autenticada; el campo tiene que existir para que
// html/template no falle con "can't evaluate field Session".
type landingData struct {
	Session *session.Session
}
