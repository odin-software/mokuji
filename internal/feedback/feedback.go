// Package feedback implementa el formulario opt-in de feedback que envía
// al destino configurado por FEEDBACK_EMAIL_TO. Si la env var está vacía,
// main.go no construye el Handler y el paquete queda silencioso: IsEnabled
// reporta false y los handlers consumidores ocultan los CTAs.
package feedback

import (
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sync/atomic"

	"git.odin.do/kengru/mokuji/internal/csrf"
	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/flash"
	"git.odin.do/kengru/mokuji/internal/ratelimit"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/version"
)

// enabled es el flag global del feature. Se accede atómicamente para que
// los reads concurrentes de templates renderizándose no se carreen contra
// el set inicial en main.go.
var enabled atomic.Bool

// IsEnabled reporta si el feature está activo. Las FuncMaps de los handlers
// que renderizan layout registran un "feedbackEnabled" que delega aquí.
func IsEnabled() bool {
	return enabled.Load()
}

// SetEnabled flipa el flag. Llamar una vez al startup desde main.go.
func SetEnabled(v bool) {
	enabled.Store(v)
}

// Config agrupa las dependencias del Handler. Todos los campos son
// required cuando se construye con New.
type Config struct {
	Templates fs.FS // web/templates/ con layout.html + feedback.html
	Email     *email.Client
	// EmailTo es el destino donde caen los emails de feedback. Viene de
	// la env var FEEDBACK_EMAIL_TO.
	EmailTo string
	Limiter *ratelimit.Limiter
	Flasher *flash.Flasher
	Logger  *slog.Logger
}

// Handler sirve GET y POST /feedback. Sólo se construye cuando el
// feature está activo (env var seteada).
type Handler struct {
	tmpl    *template.Template
	email   *email.Client
	emailTo string
	limiter *ratelimit.Limiter
	flasher *flash.Flasher
	logger  *slog.Logger
}

// New construye un Handler. Falla si los templates no parsean o si los
// campos obligatorios están vacíos.
func New(cfg Config) (*Handler, error) {
	if cfg.Templates == nil {
		return nil, errors.New("Templates requerido")
	}
	if cfg.Email == nil {
		return nil, errors.New("Email requerido")
	}
	if cfg.EmailTo == "" {
		return nil, errors.New("EmailTo requerido")
	}
	if cfg.Limiter == nil {
		return nil, errors.New("Limiter requerido")
	}
	if cfg.Flasher == nil {
		return nil, errors.New("Flasher requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	funcs := template.FuncMap{
		"csrfInput":  csrf.Input,
		"appVersion": func() string { return version.Value },
	}
	tmpl, err := template.New("base").Funcs(funcs).ParseFS(
		cfg.Templates, "layout.html", "feedback.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parsear templates feedback: %w", err)
	}

	return &Handler{
		tmpl:    tmpl,
		email:   cfg.Email,
		emailTo: cfg.EmailTo,
		limiter: cfg.Limiter,
		flasher: cfg.Flasher,
		logger:  cfg.Logger,
	}, nil
}

// Mount registra GET y POST /feedback en el mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /feedback", h.get)
	mux.HandleFunc("POST /feedback", h.post)
}

type formData struct {
	Session *session.Session
	Error   string // mensaje a renderizar arriba del form (validación)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		// El middleware de sesión debería haber redirigido antes de llegar
		// aquí; defensa en profundidad.
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "layout", formData{Session: s}); err != nil {
		h.logger.Error("renderizar feedback", "err", err)
	}
}

// post es stub por ahora; lo completamos en Task 3.
func (h *Handler) post(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
