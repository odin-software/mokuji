// Package admin expone endpoints internos protegidos por bearer token, para
// disparar operaciones manuales en producción (test de digest, eventualmente
// otros utilitarios).
//
// El token vive en la env var ADMIN_TOKEN. La comparación es en tiempo
// constante para no leak via timing. El endpoint NO usa sesiones — el token
// es la única credencial. Si ADMIN_TOKEN está vacío, todos los endpoints
// responden 503 (feature deshabilitada).
package admin

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"git.odin.do/kengru/mokuji/internal/grant"
	"git.odin.do/kengru/mokuji/internal/httpx"
	"git.odin.do/kengru/mokuji/internal/ratelimit"
	"git.odin.do/kengru/mokuji/internal/releases/digest"
	"git.odin.do/kengru/mokuji/internal/releases/worker"
)

// HealthReporter abstrae al productor del snapshot de salud de sources.
// Lo cumple *worker.Worker en producción; tests inyectan stubs.
type HealthReporter interface {
	Health() map[string]worker.SourceHealth
}

// Config agrupa las dependencias del handler.
type Config struct {
	DB     *sql.DB
	Token  string // ADMIN_TOKEN; vacío deshabilita los endpoints
	Digest *digest.Sender
	// Health es opcional. Si está cableado, /admin/sources-health responde
	// con el snapshot. Si es nil, ese endpoint no se monta.
	Health HealthReporter
	Logger *slog.Logger
	// BaseURL es el origen público (ej: https://mokuji.org). Se usa para
	// construir la URL de canje que devuelve /admin/grant-session.
	BaseURL string
	// GrantLimiter acota /admin/grant-session por IP. nil = sin límite.
	GrantLimiter *ratelimit.Limiter
	// TrustProxy=true hace que el limiter lea X-Forwarded-For para
	// identificar al cliente. Solo seguro detrás de un proxy de confianza.
	TrustProxy bool
}

// Handler sirve los endpoints /admin/*.
type Handler struct {
	db         *sql.DB
	token      string
	digest     *digest.Sender
	health     HealthReporter
	logger     *slog.Logger
	baseURL    string
	grantLim   *ratelimit.Limiter
	trustProxy bool
}

// New construye un Handler. Falla si Digest es nil (no hay nada útil que
// hacer sin él).
func New(cfg Config) (*Handler, error) {
	if cfg.Digest == nil {
		return nil, errors.New("Digest requerido")
	}
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Handler{
		db:         cfg.DB,
		token:      cfg.Token,
		digest:     cfg.Digest,
		health:     cfg.Health,
		logger:     cfg.Logger,
		baseURL:    cfg.BaseURL,
		grantLim:   cfg.GrantLimiter,
		trustProxy: cfg.TrustProxy,
	}, nil
}

// Mount registra los endpoints.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/trigger-digest", h.triggerDigest)
	mux.HandleFunc("POST /admin/grant-session", h.grantSession)
	if h.health != nil {
		mux.HandleFunc("GET /admin/sources-health", h.sourcesHealth)
	}
}

// sourcesHealth devuelve el snapshot in-memory de cómo está respondiendo
// cada source de release tracking. Útil para diagnosticar regresiones
// del scraper de Asura cuando cambian el HTML, o detectar rate limits
// crónicos de MangaDex.
//
// Sin alerta proactiva: el operador chequea cuando un manga se queda
// atrás. ADR 0014 §8 lo deja deliberadamente fuera para no email-spamear
// con downtime corto.
func (h *Handler) sourcesHealth(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "no autorizado", http.StatusUnauthorized)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"sources": h.health.Health(),
	})
}

// triggerDigest dispara un digest manualmente para un user. Body o query:
//   - user_id=N o email=foo@bar
//
// Útil para QA: el operador puede comprobar que el render es correcto sin
// esperar al domingo. Idempotente: si el user ya recibió esta semana,
// responde 204 "sin caps nuevos".
func (h *Handler) triggerDigest(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "no autorizado", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	userID, err := h.resolveUserID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch err := h.digest.SendToUser(r.Context(), userID); {
	case err == nil:
		respondJSON(w, http.StatusOK, map[string]any{"status": "sent", "user_id": userID})
	case errors.Is(err, digest.ErrNoNewChapters):
		respondJSON(w, http.StatusNoContent, nil)
	case errors.Is(err, digest.ErrDigestDisabled):
		http.Error(w, "user tiene digest desactivado", http.StatusConflict)
	default:
		h.logger.Error("admin trigger-digest", "user_id", userID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// grantSession emite un token de un solo uso que canjea GET /auth/grant por
// una sesión. Es el break-glass para cuando el envío de email está caído y
// nadie —ni el operador— puede completar el flow de OTP.
//
// Es un bypass de auth completo, así que está apretado a propósito:
//   - solo emite para users que YA existen; un ADMIN_TOKEN filtrado permite
//     impersonar cuentas existentes, no fabricar cuentas nuevas
//   - rate limit por IP encima del bearer token
//   - WARN en cada emisión, con IP y email, para que quede rastro
func (h *Handler) grantSession(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "no autorizado", http.StatusUnauthorized)
		return
	}
	if !h.grantLim.Allow(h.clientIP(r)) {
		http.Error(w, "demasiados intentos", http.StatusTooManyRequests)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	emailAddr := strings.TrimSpace(r.FormValue("email"))
	if emailAddr == "" {
		http.Error(w, "email requerido", http.StatusBadRequest)
		return
	}

	var userID int64
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM users WHERE email = ?`, emailAddr,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "user no existe", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("admin grant-session: buscar user", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	token, err := grant.Issue(r.Context(), h.db, userID)
	if err != nil {
		h.logger.Error("admin grant-session: emitir grant", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.logger.Warn("grant de sesión emitido",
		"user_id", userID, "email", emailAddr, "ip", h.clientIP(r))

	respondJSON(w, http.StatusOK, map[string]any{
		"url":        h.baseURL + "/auth/grant?t=" + url.QueryEscape(token),
		"expires_in": grant.TTL.String(),
	})
}

// clientIP identifica al cliente para el rate limiter.
func (h *Handler) clientIP(r *http.Request) string {
	return httpx.ClientIP(r, h.trustProxy)
}

// authorized chequea el header Authorization: Bearer <token>. Tiempo constante.
// Si el token configurado está vacío, falla — endpoint deshabilitado.
func (h *Handler) authorized(r *http.Request) bool {
	if h.token == "" {
		return false
	}
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	got := auth[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

// resolveUserID acepta user_id (preferido, evita lookup) o email (cómodo en
// CLI). Si ambos están presentes, user_id gana.
func (h *Handler) resolveUserID(r *http.Request) (int64, error) {
	if raw := r.PostFormValue("user_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return 0, errors.New("user_id inválido")
		}
		return id, nil
	}
	if email := r.PostFormValue("email"); email != "" {
		var id int64
		err := h.db.QueryRowContext(r.Context(),
			`SELECT id FROM users WHERE email = ? AND deleted_at IS NULL`,
			email,
		).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("user no encontrado")
		}
		if err != nil {
			return 0, errors.New("error buscando user")
		}
		return id, nil
	}
	return 0, errors.New("se requiere user_id o email")
}

func respondJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}
