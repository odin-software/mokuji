// Package auth implementa el flow de autenticación con email OTP (ADR 0004).
//
// Endpoints:
//   GET  /auth         -> form de email
//   POST /auth/start   -> recibir email, generar código, enviar por email
//   GET  /auth/verify  -> form de código
//   POST /auth/verify  -> validar código, crear/buscar usuario, crear sesión
//   POST /auth/logout  -> destruir sesión
package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	htmltmpl "html/template"
	"io/fs"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	texttmpl "text/template"
	"time"

	"git.odin.do/kengru/mokuji/internal/csrf"
	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/session"
)

const (
	codeLength   = 6
	codeDuration = 10 * time.Minute
)

// Config agrupa las dependencias del handler.
type Config struct {
	DB              *sql.DB
	Sessions        *session.Manager
	Email           *email.Client
	HTMLTemplates   fs.FS // contiene layout.html y auth/*.html
	EmailTemplates  fs.FS // contiene otp.txt
	BaseURL         string
	Logger          *slog.Logger
}

// Handler agrupa endpoints de /auth/*.
type Handler struct {
	db       *sql.DB
	sessions *session.Manager
	email    *email.Client
	pages    map[string]*htmltmpl.Template
	emailT   *texttmpl.Template
	baseURL  string
	logger   *slog.Logger
}

// New construye un Handler. Falla si las plantillas no parsean.
func New(cfg Config) (*Handler, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Sessions == nil {
		return nil, errors.New("Sessions requerido")
	}
	if cfg.Email == nil {
		return nil, errors.New("Email requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	funcs := htmltmpl.FuncMap{
		"csrfInput": csrf.Input,
	}

	base, err := htmltmpl.New("base").Funcs(funcs).ParseFS(cfg.HTMLTemplates, "layout.html")
	if err != nil {
		return nil, fmt.Errorf("parsear layout: %w", err)
	}

	pages := map[string]*htmltmpl.Template{}
	for _, page := range []string{"auth/start.html", "auth/verify.html"} {
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("clonar base para %s: %w", page, err)
		}
		if _, err := clone.ParseFS(cfg.HTMLTemplates, page); err != nil {
			return nil, fmt.Errorf("parsear %s: %w", page, err)
		}
		pages[page] = clone
	}

	emailT, err := texttmpl.ParseFS(cfg.EmailTemplates, "otp.txt")
	if err != nil {
		return nil, fmt.Errorf("parsear otp.txt: %w", err)
	}

	return &Handler{
		db:       cfg.DB,
		sessions: cfg.Sessions,
		email:    cfg.Email,
		pages:    pages,
		emailT:   emailT,
		baseURL:  cfg.BaseURL,
		logger:   cfg.Logger,
	}, nil
}

// Mount registra los endpoints en el mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth", h.startForm)
	mux.HandleFunc("POST /auth/start", h.start)
	mux.HandleFunc("GET /auth/verify", h.verifyForm)
	mux.HandleFunc("POST /auth/verify", h.verify)
	mux.HandleFunc("POST /auth/logout", h.logout)
}

// startData es el modelo de datos para auth/start.html.
type startData struct {
	Email string
	Error string
}

// verifyData es el modelo de datos para auth/verify.html.
type verifyData struct {
	Email string
	Error string
}

// otpEmailData es el modelo para otp.txt.
type otpEmailData struct {
	Code        string
	To          string
	IPAddress   string
	UserAgent   string
	RequestedAt string
	BaseURL     string
}

func (h *Handler) startForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := session.FromContext(r.Context()); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.render(w, "auth/start.html", startData{Email: r.URL.Query().Get("email")})
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	emailAddr := strings.TrimSpace(r.PostFormValue("email"))
	if _, err := mail.ParseAddress(emailAddr); err != nil {
		h.render(w, "auth/start.html", startData{
			Email: emailAddr,
			Error: "ingresa un email válido",
		})
		return
	}

	code, err := generateCode()
	if err != nil {
		h.logger.Error("generar código", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ip := clientIP(r)
	ua := r.UserAgent()

	if err := h.storeCode(r.Context(), emailAddr, code, ip, ua); err != nil {
		h.logger.Error("guardar código", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	body, err := h.renderOTPEmail(code, emailAddr, ip, ua)
	if err != nil {
		h.logger.Error("renderizar email otp", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.email.Send(emailAddr, "tu código de mokuji", body); err != nil {
		h.logger.Error("enviar email", "err", err, "to", emailAddr)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/auth/verify?email="+url.QueryEscape(emailAddr), http.StatusSeeOther)
}

func (h *Handler) verifyForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := session.FromContext(r.Context()); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	emailAddr := r.URL.Query().Get("email")
	if emailAddr == "" {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}
	h.render(w, "auth/verify.html", verifyData{Email: emailAddr})
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	emailAddr := strings.TrimSpace(r.PostFormValue("email"))
	code := strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("code"), " ", ""))

	if _, err := mail.ParseAddress(emailAddr); err != nil {
		h.render(w, "auth/verify.html", verifyData{Email: emailAddr, Error: "email inválido"})
		return
	}
	if !isValidCode(code) {
		h.render(w, "auth/verify.html", verifyData{Email: emailAddr, Error: "código inválido"})
		return
	}

	ok, err := h.consumeCode(r.Context(), emailAddr, code)
	if err != nil {
		h.logger.Error("validar código", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		h.render(w, "auth/verify.html", verifyData{Email: emailAddr, Error: "código inválido o expirado"})
		return
	}

	userID, err := h.findOrCreateUser(r.Context(), emailAddr)
	if err != nil {
		h.logger.Error("crear usuario", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.sessions.Create(w, r, userID); err != nil {
		h.logger.Error("crear sesión", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.sessions.Destroy(w, r); err != nil {
		h.logger.Error("destruir sesión", "err", err)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// render ejecuta la plantilla y la escribe a w. Si falla, loguea y responde 500.
func (h *Handler) render(w http.ResponseWriter, page string, data any) {
	tmpl, ok := h.pages[page]
	if !ok {
		h.logger.Error("plantilla no encontrada", "page", page)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("ejecutar plantilla", "page", page, "err", err)
	}
}

func (h *Handler) renderOTPEmail(code, to, ip, ua string) (string, error) {
	data := otpEmailData{
		Code:        formatCode(code),
		To:          to,
		IPAddress:   ip,
		UserAgent:   ua,
		RequestedAt: time.Now().UTC().Format("2006-01-02 15:04 UTC"),
		BaseURL:     h.baseURL,
	}
	var buf bytes.Buffer
	if err := h.emailT.ExecuteTemplate(&buf, "otp.txt", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (h *Handler) storeCode(ctx context.Context, email, code, ip, ua string) error {
	now := time.Now().UTC()
	expires := now.Add(codeDuration)
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO otp_codes (email, code_hash, created_at, expires_at, ip_address, user_agent)
		VALUES (?, ?, ?, ?, ?, ?)
	`, email, hashCode(code), now.Unix(), expires.Unix(), ip, ua)
	return err
}

// consumeCode valida y marca un código como usado. Retorna (true, nil) si fue
// válido y se consumió; (false, nil) si no había match válido; (_, err) si
// hubo error de DB.
func (h *Handler) consumeCode(ctx context.Context, email, code string) (bool, error) {
	hash := hashCode(code)
	now := time.Now().UTC().Unix()

	res, err := h.db.ExecContext(ctx, `
		UPDATE otp_codes
		SET used_at = ?
		WHERE email = ? AND code_hash = ? AND expires_at > ? AND used_at IS NULL
	`, now, email, hash, now)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// findOrCreateUser retorna el id del usuario con el email dado, creando uno
// nuevo si no existe.
func (h *Handler) findOrCreateUser(ctx context.Context, emailAddr string) (int64, error) {
	var id int64
	err := h.db.QueryRowContext(ctx,
		`SELECT id FROM users WHERE email = ?`, emailAddr,
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	res, err := h.db.ExecContext(ctx,
		`INSERT INTO users (email, created_at) VALUES (?, ?)`,
		emailAddr, time.Now().UTC().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// generateCode produce un string de 6 dígitos numéricos crypto-random.
func generateCode() (string, error) {
	var code [codeLength]byte
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		code[i] = byte('0' + n.Int64())
	}
	return string(code[:]), nil
}

// formatCode inserta un espacio en el medio para legibilidad en email: "123456" -> "123 456".
func formatCode(code string) string {
	if len(code) != codeLength {
		return code
	}
	return code[:3] + " " + code[3:]
}

func isValidCode(s string) bool {
	if len(s) != codeLength {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hashCode(c string) string {
	sum := sha256.Sum256([]byte(c))
	return hex.EncodeToString(sum[:])
}

// clientIP extrae solo la IP de RemoteAddr (que viene como "ip:port").
func clientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
