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
	"time"

	"git.odin.do/kengru/mokuji/internal/csrf"
	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/flash"
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
	Flasher        *flash.Flasher
	Logger         *slog.Logger
}

// Handler sirve /settings, /u/{handle}, /settings/delete-account/* y /restore-account.
type Handler struct {
	db      *sql.DB
	pages   map[string]*template.Template
	email   *email.Client
	deleteT *texttmpl.Template // template del email de borrado
	baseURL string
	flasher *flash.Flasher
	logger  *slog.Logger
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
		flasher: cfg.Flasher,
		logger:  cfg.Logger,
	}, nil
}

// Mount registra los endpoints en el mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings", h.settings)
	mux.HandleFunc("POST /settings", h.updateSettings)
	mux.HandleFunc("POST /settings/username", h.updateUsername)
	mux.HandleFunc("GET /settings/export", h.exportData)
	mux.HandleFunc("GET /settings/delete-account", h.deleteAccountPage)
	mux.HandleFunc("POST /settings/delete-account/request", h.deleteAccountRequest)
	mux.HandleFunc("POST /settings/delete-account/confirm", h.deleteAccountConfirm)
	mux.HandleFunc("GET /restore-account", h.restoreAccountPage)
	mux.HandleFunc("POST /restore-account/cancel", h.restoreAccountCancel)
	mux.HandleFunc("POST /restore-account/delete-now", h.restoreAccountDeleteNow)
	mux.HandleFunc("GET /u/{handle}", h.publicProfile)
}

// ---- /settings ----

type settingsData struct {
	Session       *session.Session
	Visibility    string
	Email         string
	DigestEnabled bool

	// Flash es el mensaje one-shot post-redirect (cookie efímera).
	// nil si no hay nada que mostrar.
	Flash *flash.Flash

	// Username UI: el slug actual (vacío si nunca lo eligió), el draft
	// pre-poblado del form en caso de validation error, el error a
	// mostrar inline, y la fecha desde la cual puede cambiarlo de nuevo
	// (si está dentro del cooldown).
	Username         string
	UsernameDraft    string
	UsernameError    string
	CooldownUntilFmt string // vacío si está fuera del cooldown
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	h.renderSettings(w, r, "", "")
}

// renderSettings centraliza la carga del estado del user y el render de
// /settings. Lo reusan tanto el GET inicial como el POST /settings/username
// cuando hay validation error y queremos preservar el draft + mostrar el
// mensaje sin perder los otros campos visibles.
//
// Importante: el GET hace flash.Pop (one-shot) — el flash desaparece
// tras el primer render. El validation-error path NO consume flash
// (porque viene del mismo request, no de un redirect).
func (h *Handler) renderSettings(w http.ResponseWriter, r *http.Request, draft, errMsg string) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	var (
		visibility, emailAddr string
		digestEnabled         int
	)
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT list_visibility, email, email_digest_enabled FROM users WHERE id = ?`, s.UserID,
	).Scan(&visibility, &emailAddr, &digestEnabled); err != nil {
		h.logger.Error("cargar settings", "err", err, "user_id", s.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slug, changedAt, err := h.loadUserSlug(r.Context(), s.UserID)
	if err != nil {
		h.logger.Error("cargar slug", "err", err, "user_id", s.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Si está dentro del cooldown, formateamos la fecha futura desde la
	// cual podrá cambiar. Sin cooldown activo el campo queda vacío y la
	// UI muestra el form normal.
	var cooldownFmt string
	if changedAt.Valid {
		nextAllowed := time.Unix(changedAt.Int64, 0).Add(UsernameCooldown)
		if time.Now().Before(nextAllowed) {
			cooldownFmt = nextAllowed.Format("2006-01-02")
		}
	}

	// Flash one-shot: solo lo consumimos cuando NO estamos re-renderizando
	// por validation error (draft/errMsg). El error inline es feedback del
	// mismo request; el flash es del request anterior (post-redirect).
	var fl *flash.Flash
	if draft == "" && errMsg == "" && h.flasher != nil {
		if kind, msg, ok := h.flasher.Pop(w, r); ok {
			fl = &flash.Flash{Kind: kind, Message: msg}
		}
	}

	h.render(w, "settings.html", settingsData{
		Session:          s,
		Visibility:       visibility,
		Email:            emailAddr,
		DigestEnabled:    digestEnabled == 1,
		Flash:            fl,
		Username:         slug.String,
		UsernameDraft:    draft,
		UsernameError:    errMsg,
		CooldownUntilFmt: cooldownFmt,
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

	if h.flasher != nil {
		h.flasher.Set(w, flash.KindSuccess, "cambios guardados.")
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// updateUsername procesa el POST del form de username. En éxito redirige a
// /settings?saved=username; en error de validación o conflicto, re-renderea
// /settings con el draft + el mensaje, sin perder los otros campos visibles.
//
// Los errores se mapean a mensajes en español acá (presentación), mientras
// que ValidateUsername/upsertUsername solo manejan errores de dominio. Eso
// permite reusar el dominio sin atar la traducción.
func (h *Handler) updateUsername(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	raw := r.PostFormValue("username")

	slug, err := ValidateUsername(raw)
	if err != nil {
		h.renderSettings(w, r, raw, usernameErrorMessage(err))
		return
	}

	switch err := h.upsertUsername(r.Context(), s.UserID, slug, time.Now()); {
	case err == nil:
		if h.flasher != nil {
			h.flasher.Set(w, flash.KindSuccess, "username actualizado.")
		}
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	case errors.Is(err, ErrUsernameTaken),
		errors.Is(err, ErrUsernameCooldown):
		h.renderSettings(w, r, raw, usernameErrorMessage(err))
	default:
		h.logger.Error("actualizar username", "err", err, "user_id", s.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// usernameErrorMessage traduce los errores tipados del dominio a mensajes
// listos para mostrar en la UI. Si el error no matchea ninguno conocido,
// devolvemos un genérico — no leakeamos detalles internos.
func usernameErrorMessage(err error) string {
	switch {
	case errors.Is(err, ErrUsernameTooShort):
		return "muy corto. mínimo 3 caracteres."
	case errors.Is(err, ErrUsernameTooLong):
		return "muy largo. máximo 20 caracteres."
	case errors.Is(err, ErrUsernameInvalid):
		return "solo letras minúsculas, números, guión y guión bajo. no puede empezar ni terminar con separador."
	case errors.Is(err, ErrUsernameDigitsOnly):
		return "no puede ser solo números (chocaría con URLs viejas /u/{id})."
	case errors.Is(err, ErrUsernameReserved):
		return "ese nombre está reservado por la app. prueba otro."
	case errors.Is(err, ErrUsernameTaken):
		return "ya está tomado por otro usuario."
	case errors.Is(err, ErrUsernameCooldown):
		return "lo cambiaste hace poco. puedes volver a cambiarlo después del cooldown de 30 días."
	default:
		return "no se pudo guardar el username."
	}
}

// ---- /u/{id} ----

type profileData struct {
	Session    *session.Session
	UserID     int64
	Slug       string // vacío si el user no tiene slug todavía
	IsOwner    bool
	Visibility string
	Entries    []ProfileEntry
}

// publicProfile resuelve dos formatos para {handle}:
//
//   - Numérico (legacy /u/{id}): si el user tiene slug, 301 a /u/{slug}
//     para canonicalizar; si no tiene slug, sirve el perfil con el id.
//   - String (nuevo /u/{slug}): lookup por slug actual. previous_slug NO
//     resuelve — esos están reservados pero inaccesibles, para no leakear
//     identidad post-rename. Match → sirve; sin match → 404.
//
// Validamos digits-only en slug en POST /settings/username así que la
// rama numérica es seguro asumirla como id legacy.
func (h *Handler) publicProfile(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")

	var (
		userID int64
		slug   sql.NullString
	)
	if id, err := strconv.ParseInt(handle, 10, 64); err == nil {
		// Rama numérica: cargamos id + slug; si tiene slug, 301.
		userID = id
		if err := h.db.QueryRowContext(r.Context(),
			`SELECT slug FROM users WHERE id = ? AND deleted_at IS NULL`,
			id,
		).Scan(&slug); errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			h.logger.Error("cargar usuario por id", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if slug.Valid {
			http.Redirect(w, r, "/u/"+slug.String, http.StatusMovedPermanently)
			return
		}
	} else {
		// Rama slug: lookupUserBySlug filtra deleted_at IS NULL.
		id, err := h.lookupUserBySlug(r.Context(), handle)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.logger.Error("cargar usuario por slug", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		userID = id
		slug = sql.NullString{String: handle, Valid: true}
	}

	var visibility string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT list_visibility FROM users WHERE id = ?`, userID,
	).Scan(&visibility); err != nil {
		h.logger.Error("cargar visibility", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s, _ := session.FromContext(r.Context())
	isOwner := s != nil && s.UserID == userID

	if visibility != VisibilityPublic && !isOwner {
		http.NotFound(w, r)
		return
	}

	entries, err := h.loadEntries(r.Context(), userID)
	if err != nil {
		h.logger.Error("cargar entries de perfil", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.render(w, "profile.html", profileData{
		Session:    s,
		UserID:     userID,
		Slug:       slug.String,
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
