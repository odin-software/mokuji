package unsubscribe

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"git.odin.do/kengru/mokuji/internal/csrf"
	"git.odin.do/kengru/mokuji/internal/feedback"
	"git.odin.do/kengru/mokuji/internal/ratelimit"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/version"
)

// Config agrupa las dependencias del handler.
type Config struct {
	DB            *sql.DB
	Signer        *Signer
	HTMLTemplates fs.FS
	// IPLimiter aplica a ambos GET y POST /unsubscribe. Cada hit valida un
	// HMAC, así que un fuzzer puede enumerar tokens si lo dejamos abierto.
	// nil = sin rate limiting (útil en tests).
	IPLimiter *ratelimit.Limiter
	// TrustProxy: leer IP del cliente desde X-Forwarded-For. Solo seguro
	// si nginx (u otro proxy de confianza) está delante y limpia el
	// header. Sin proxy de confianza, false → usar RemoteAddr.
	TrustProxy bool
	Logger     *slog.Logger
}

// Handler sirve /unsubscribe (GET confirmación + POST one-click).
//
// El endpoint funciona sin sesión: el token HMAC es la única credencial.
// Si el user llega con cookie de sesión activa, igual se valida por token —
// no usamos la sesión para autorizar. Esto cumple con RFC 8058 (One-Click),
// donde el cliente de email (Gmail, Apple) hace el POST sin cookies.
type Handler struct {
	db         *sql.DB
	signer     *Signer
	tmpl       *template.Template
	ipLimiter  *ratelimit.Limiter
	trustProxy bool
	logger     *slog.Logger
}

// New construye un Handler. Falla si templates no parsean o si signer es nil.
func New(cfg Config) (*Handler, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Signer == nil {
		return nil, errors.New("Signer requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	funcs := template.FuncMap{
		"csrfInput":       csrf.Input,
		"appVersion":      func() string { return version.Value },
		"feedbackEnabled": feedback.IsEnabled,
	}
	base, err := template.New("base").Funcs(funcs).ParseFS(cfg.HTMLTemplates, "layout.html")
	if err != nil {
		return nil, fmt.Errorf("parsear layout: %w", err)
	}
	page, err := base.Clone()
	if err != nil {
		return nil, fmt.Errorf("clonar base: %w", err)
	}
	if _, err := page.ParseFS(cfg.HTMLTemplates, "unsubscribe.html"); err != nil {
		return nil, fmt.Errorf("parsear unsubscribe.html: %w", err)
	}

	return &Handler{
		db:         cfg.DB,
		signer:     cfg.Signer,
		tmpl:       page,
		ipLimiter:  cfg.IPLimiter,
		trustProxy: cfg.TrustProxy,
		logger:     cfg.Logger,
	}, nil
}

// clientIP devuelve el IP real del cliente. Mismo helper que auth.Handler —
// si trustProxy está activo, lee el primer IP de X-Forwarded-For (asumiendo
// un solo proxy delante). Sin proxy de confianza, usa RemoteAddr stripping
// el puerto.
func (h *Handler) clientIP(r *http.Request) string {
	if h.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.Index(xff, ","); i != -1 {
				xff = xff[:i]
			}
			return strings.TrimSpace(xff)
		}
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// allowOrReject chequea el rate limiter y, si está rate-limited, responde
// 429 al cliente. Devuelve true si la request puede continuar, false si
// ya se respondió. Llamar al inicio de cada handler protegido.
func (h *Handler) allowOrReject(w http.ResponseWriter, r *http.Request) bool {
	if h.ipLimiter.Allow(h.clientIP(r)) {
		return true
	}
	http.Error(w, "demasiados intentos, espera unos minutos", http.StatusTooManyRequests)
	return false
}

// Mount registra los endpoints.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /unsubscribe", h.page)
	mux.HandleFunc("POST /unsubscribe", h.submit)
}

// pageState comunica al template el resultado de la validación del token.
type pageState string

const (
	stateValid   pageState = "valid"   // GET con token válido: mostrar form de confirmación
	stateInvalid pageState = "invalid" // token mal formado, firma incorrecta, scope inválido
	stateExpired pageState = "expired" // token > MaxAge
	stateDone    pageState = "done"    // POST exitoso: digest desactivado
)

type pageData struct {
	Session *session.Session
	State   pageState
	Token   string
}

// page renderiza la página de confirmación. Validamos el token en el GET para
// dar feedback temprano si el link está roto/expirado; un user con token
// válido ve "confirmar" en lugar de "click ciego".
func (h *Handler) page(w http.ResponseWriter, r *http.Request) {
	if !h.allowOrReject(w, r) {
		return
	}
	s, _ := session.FromContext(r.Context())
	token := r.URL.Query().Get("token")

	data := pageData{Session: s, Token: token}
	if token == "" {
		data.State = stateInvalid
		h.render(w, data)
		return
	}

	if _, err := h.signer.Verify(token, ScopeReleasesDigest); err != nil {
		if errors.Is(err, ErrExpiredToken) {
			data.State = stateExpired
		} else {
			data.State = stateInvalid
		}
		h.render(w, data)
		return
	}

	data.State = stateValid
	h.render(w, data)
}

// submit procesa el opt-out. Acepta el token en form, query o ambos —
// browsers vienen con form (POST clásico), clientes One-Click vienen con
// token en query y body "List-Unsubscribe=One-Click" (RFC 8058 §4).
//
// Idempotente: un token válido cuyo user ya tiene digest=0 retorna stateDone
// sin error. Nunca revelamos si el user existe o si ya estaba desuscrito.
func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	if !h.allowOrReject(w, r) {
		return
	}
	s, _ := session.FromContext(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	token := r.PostFormValue("token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}

	data := pageData{Session: s, Token: token}
	if token == "" {
		data.State = stateInvalid
		h.render(w, data)
		return
	}

	userID, err := h.signer.Verify(token, ScopeReleasesDigest)
	if err != nil {
		if errors.Is(err, ErrExpiredToken) {
			data.State = stateExpired
		} else {
			data.State = stateInvalid
		}
		h.render(w, data)
		return
	}

	// Apagamos el digest. Si el user ya tenía digest=0 o fue borrado,
	// RowsAffected será 0 — sigue siendo éxito para el caller.
	if _, err := h.db.ExecContext(r.Context(),
		`UPDATE users SET email_digest_enabled = 0 WHERE id = ? AND deleted_at IS NULL`,
		userID,
	); err != nil {
		h.logger.Error("desactivar digest", "user_id", userID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.logger.Info("unsubscribe", "user_id", userID, "scope", ScopeReleasesDigest, "at", time.Now().UTC())
	data.State = stateDone
	h.render(w, data)
}

func (h *Handler) render(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("renderizar unsubscribe", "err", err)
	}
}
