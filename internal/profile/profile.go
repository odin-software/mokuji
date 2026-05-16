// Package profile sirve los endpoints relacionados al perfil del usuario:
//   - GET/POST /settings -> editar preferencias propias (visibilidad de lista)
//   - GET /u/{id}        -> vista pública read-only de la biblioteca de un usuario
//
// La visibilidad se controla con la columna users.list_visibility
// ('public' | 'private'). Si la lista es privada y el viewer no es el dueño,
// retornamos 404 (no 403) para no confirmar que el usuario existe.
package profile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	texttmpl "text/template"

	"git.odin.do/kengru/mokuji/internal/csrf"
	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/version"
)

// Visibility values válidos para users.list_visibility.
const (
	VisibilityPublic  = "public"
	VisibilityPrivate = "private"
)

// Manga + ProfileEntry son las versiones simplificadas para vista pública.
// No incluyen source_url ni notes (datos privados del owner).
type Manga struct {
	ID       int64
	Title    string
	CoverURL string
}

type ProfileEntry struct {
	Manga       Manga
	Status      string
	LastChapter string
	IsFavorite  bool
}

// Config agrupa las dependencias del handler.
type Config struct {
	DB             *sql.DB
	HTMLTemplates  fs.FS
	EmailTemplates fs.FS         // contiene account-deletion.txt; opcional si Email es nil
	Email          *email.Client // opcional; sin él, los endpoints de borrado responden 503
	BaseURL        string        // usado en el email de borrado
	Logger         *slog.Logger
}

// Handler sirve /settings, /u/{id}, /settings/delete-account/* y /restore-account.
type Handler struct {
	db         *sql.DB
	pages      map[string]*template.Template
	email      *email.Client
	deleteT    *texttmpl.Template // template del email de borrado
	baseURL    string
	logger     *slog.Logger
}

// New construye un Handler. Falla si templates no parsean.
func New(cfg Config) (*Handler, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	funcs := template.FuncMap{
		"csrfInput":   csrf.Input,
		"statusLabel": statusLabel,
		"appVersion":  func() string { return version.Value },
	}
	base, err := template.New("base").Funcs(funcs).ParseFS(cfg.HTMLTemplates, "layout.html")
	if err != nil {
		return nil, fmt.Errorf("parsear layout: %w", err)
	}

	pages := map[string]*template.Template{}
	for _, page := range []string{"settings.html", "profile.html", "delete-account.html", "restore-account.html"} {
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("clonar base para %s: %w", page, err)
		}
		if _, err := clone.ParseFS(cfg.HTMLTemplates, page); err != nil {
			return nil, fmt.Errorf("parsear %s: %w", page, err)
		}
		pages[page] = clone
	}

	// El template de email es opcional para que tests que no necesitan
	// borrado puedan construir el Handler sin EmailTemplates.
	var deleteT *texttmpl.Template
	if cfg.EmailTemplates != nil {
		deleteT, err = texttmpl.ParseFS(cfg.EmailTemplates, "account-deletion.txt")
		if err != nil {
			return nil, fmt.Errorf("parsear account-deletion.txt: %w", err)
		}
	}

	return &Handler{
		db:      cfg.DB,
		pages:   pages,
		email:   cfg.Email,
		deleteT: deleteT,
		baseURL: cfg.BaseURL,
		logger:  cfg.Logger,
	}, nil
}

// Mount registra los endpoints en el mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings", h.settings)
	mux.HandleFunc("POST /settings", h.updateSettings)
	mux.HandleFunc("GET /settings/export", h.exportData)
	mux.HandleFunc("GET /settings/delete-account", h.deleteAccountPage)
	mux.HandleFunc("POST /settings/delete-account/request", h.deleteAccountRequest)
	mux.HandleFunc("POST /settings/delete-account/confirm", h.deleteAccountConfirm)
	mux.HandleFunc("GET /restore-account", h.restoreAccountPage)
	mux.HandleFunc("POST /restore-account/cancel", h.restoreAccountCancel)
	mux.HandleFunc("POST /restore-account/delete-now", h.restoreAccountDeleteNow)
	mux.HandleFunc("GET /u/{id}", h.publicProfile)
}

// ---- /settings ----

type settingsData struct {
	Session       *session.Session
	Visibility    string
	Email         string
	DigestEnabled bool
	Saved         bool
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	var (
		visibility, emailAddr string
		digestEnabled         int
	)
	err := h.db.QueryRowContext(r.Context(),
		`SELECT list_visibility, email, email_digest_enabled FROM users WHERE id = ?`, s.UserID,
	).Scan(&visibility, &emailAddr, &digestEnabled)
	if err != nil {
		h.logger.Error("cargar settings", "err", err, "user_id", s.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.render(w, "settings.html", settingsData{
		Session:       s,
		Visibility:    visibility,
		Email:         emailAddr,
		DigestEnabled: digestEnabled == 1,
		Saved:         r.URL.Query().Get("saved") == "1",
	})
}

func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	vis := r.PostFormValue("list_visibility")
	if vis != VisibilityPublic && vis != VisibilityPrivate {
		http.Error(w, "valor inválido para list_visibility", http.StatusBadRequest)
		return
	}

	digestEnabled := r.PostFormValue("email_digest_enabled") == "on"

	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE users
		SET list_visibility = ?, email_digest_enabled = ?
		WHERE id = ?
	`, vis, boolToInt(digestEnabled), s.UserID); err != nil {
		h.logger.Error("actualizar settings", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- /u/{id} ----

type profileData struct {
	Session    *session.Session
	UserID     int64
	IsOwner    bool
	Visibility string
	Entries    []ProfileEntry
}

func (h *Handler) publicProfile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var visibility string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT list_visibility FROM users WHERE id = ?`, id,
	).Scan(&visibility)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("cargar usuario", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s, _ := session.FromContext(r.Context())
	isOwner := s != nil && s.UserID == id

	// Si es privada y no sos el dueño, 404 (no confirmamos que existe).
	if visibility != VisibilityPublic && !isOwner {
		http.NotFound(w, r)
		return
	}

	entries, err := h.loadEntries(r.Context(), id)
	if err != nil {
		h.logger.Error("cargar entries de perfil", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.render(w, "profile.html", profileData{
		Session:    s,
		UserID:     id,
		IsOwner:    isOwner,
		Visibility: visibility,
		Entries:    entries,
	})
}

// ---- helpers ----

func (h *Handler) requireSession(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return nil, false
	}
	return s, true
}

func (h *Handler) loadEntries(ctx context.Context, userID int64) ([]ProfileEntry, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT m.id, m.title, m.cover_url,
		       le.status, le.last_chapter, le.is_favorite
		FROM library_entries le
		JOIN manga m ON m.id = le.manga_id
		WHERE le.user_id = ?
		ORDER BY le.is_favorite DESC, le.updated_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ProfileEntry
	for rows.Next() {
		var e ProfileEntry
		var lastCh, coverURL sql.NullString
		var isFav int
		if err := rows.Scan(&e.Manga.ID, &e.Manga.Title, &coverURL, &e.Status, &lastCh, &isFav); err != nil {
			return nil, err
		}
		e.Manga.CoverURL = coverURL.String
		e.LastChapter = lastCh.String
		e.IsFavorite = isFav == 1
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (h *Handler) render(w http.ResponseWriter, page string, data any) {
	tmpl, ok := h.pages[page]
	if !ok {
		h.logger.Error("plantilla no encontrada", "page", page)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("renderizar plantilla", "page", page, "err", err)
	}
}

// statusLabel duplicado de library para que profile no dependa de library.
// Si esto crece, mover a un helper compartido.
func statusLabel(s string) string {
	switch s {
	case "reading":
		return "leyendo"
	case "planned":
		return "por leer"
	case "completed":
		return "completado"
	}
	return s
}
