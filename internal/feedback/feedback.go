// Package feedback implementa el formulario opt-in de feedback que envía
// al destino configurado por FEEDBACK_EMAIL_TO. Si la env var está vacía,
// main.go no construye el Handler y el paquete queda silencioso: IsEnabled
// reporta false y los handlers consumidores ocultan los CTAs.
package feedback

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
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
	// UserEmail mapea un user_id a su email. Se inyecta en lugar de un *sql.DB
	// para que el paquete feedback quede libre de dependencia con la capa de
	// persistencia. En prod, main.go construye una closure que consulta la
	// tabla users; los tests stubean con una función fija.
	UserEmail func(ctx context.Context, userID int64) (string, error)
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

	userEmail func(ctx context.Context, userID int64) (string, error)
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
	if cfg.UserEmail == nil {
		return nil, errors.New("UserEmail requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	funcs := template.FuncMap{
		"csrfInput":       csrf.Input,
		"appVersion":      func() string { return version.Value },
		"feedbackEnabled": IsEnabled,
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

		userEmail: cfg.UserEmail,
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

const (
	minMessage = 4
	maxMessage = 4000
)

var validTipos = map[string]bool{
	"bug":     true,
	"feature": true,
	"copy":    true,
	"otro":    true,
}

func (h *Handler) post(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}

	// Rate limit por user_id. La clave es deliberadamente prefijada para
	// no colisionar con buckets de otros endpoints (auth/start, etc.) si
	// más adelante un solo Limiter sirve a varios features.
	key := fmt.Sprintf("feedback:%d", s.UserID)
	if !h.limiter.Allow(key) {
		h.renderError(w, s, "demasiado feedback en poco tiempo. espera un rato y vuelve a intentar", http.StatusTooManyRequests)
		return
	}

	if err := r.ParseForm(); err != nil {
		h.renderError(w, s, "no pude leer el formulario, vuelve a intentarlo", http.StatusBadRequest)
		return
	}

	tipo := strings.TrimSpace(r.PostFormValue("tipo"))
	mensaje := strings.TrimSpace(r.PostFormValue("mensaje"))

	if !validTipos[tipo] {
		h.renderError(w, s, "elige un tipo válido (bug / feature / copy / otro)", http.StatusBadRequest)
		return
	}
	if len(mensaje) < minMessage {
		h.renderError(w, s, fmt.Sprintf("el mensaje es muy corto (mínimo %d caracteres)", minMessage), http.StatusBadRequest)
		return
	}
	if len(mensaje) > maxMessage {
		h.renderError(w, s, fmt.Sprintf("el mensaje es muy largo (máximo %d caracteres)", maxMessage), http.StatusBadRequest)
		return
	}

	userEmail, err := h.userEmail(r.Context(), s.UserID)
	if err != nil {
		h.logger.Error("resolver email del user para feedback", "user_id", s.UserID, "err", err)
		h.renderError(w, s, "no pude procesar tu envío, intenta de nuevo en un minuto", http.StatusInternalServerError)
		return
	}

	if err := h.sendEmail(s, userEmail, tipo, mensaje, r); err != nil {
		h.logger.Error("enviar feedback", "user_id", s.UserID, "err", err)
		h.renderError(w, s, "no pude enviar el email, intenta de nuevo en un minuto", http.StatusInternalServerError)
		return
	}

	h.flasher.Set(w, "success", "gracias, recibí tu feedback")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) renderError(w http.ResponseWriter, s *session.Session, msg string, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = h.tmpl.ExecuteTemplate(w, "layout", formData{Session: s, Error: msg})
}

func (h *Handler) sendEmail(s *session.Session, userEmail, tipo, mensaje string, r *http.Request) error {
	ua := r.UserAgent()
	if ua == "" {
		ua = "(unknown)"
	}
	plain := fmt.Sprintf(`De: %s (user_id: %d)
Tipo: %s
User-Agent: %s

%s
`, userEmail, s.UserID, tipo, ua, mensaje)

	return h.email.SendMessage(email.Message{
		To:        h.emailTo,
		Subject:   "[mokuji feedback] " + tipo,
		PlainBody: plain,
		ExtraHeaders: map[string]string{
			"Reply-To": userEmail,
		},
	})
}
