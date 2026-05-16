package profile

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"time"

	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/session"
)

// Flujo de borrado (ADR 0013 §11.2):
//
//   1. user (con sesión) -> GET /settings/delete-account
//      ve la página de info y el botón "enviarme un código".
//   2. POST /settings/delete-account/request
//      generamos código numérico de 6 dígitos, lo guardamos hasheado en
//      deletion_codes, mandamos email "para confirmar el borrado, ingresá
//      este código".
//   3. user ingresa código -> POST /settings/delete-account/confirm
//      validamos, marcamos users.deleted_at = now(), destruimos las
//      sesiones del user (incluyendo la actual) y redirigimos a /.
//   4. si el user (con cuenta deleted) intenta loguear de nuevo dentro de
//      los 7 días, el middleware lo manda a /restore-account.
//
// Decisiones:
//   - Tabla deletion_codes separada de otp_codes para que un código de login
//     no valga como confirmación de borrado.
//   - El código está atado a user_id (no email) porque el user ya está
//     autenticado al pedirlo.
//   - El delete real es soft: ponemos deleted_at, el hard delete lo hace
//     un worker diario (ver cleanup_worker.go).

const (
	deletionCodeLength   = 6
	deletionCodeDuration = 15 * time.Minute
)

// ---- handlers ----

type deleteAccountData struct {
	Session       *session.Session
	Email         string
	CodeSent      bool
	Error         string
}

// deleteAccountPage muestra la página inicial: explicación + botón para pedir
// el código, y el form de confirmación si ya hay un código pendiente.
func (h *Handler) deleteAccountPage(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	var emailAddr string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT email FROM users WHERE id = ?`, s.UserID,
	).Scan(&emailAddr); err != nil {
		h.logger.Error("delete: cargar email", "err", err, "user_id", s.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.render(w, "delete-account.html", deleteAccountData{
		Session:  s,
		Email:    emailAddr,
		CodeSent: r.URL.Query().Get("sent") == "1",
	})
}

// deleteAccountRequest genera un código de borrado y lo manda por email.
// Reusa el cliente SMTP existente (plain text, igual que el OTP de login).
func (h *Handler) deleteAccountRequest(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.email == nil || h.deleteT == nil {
		http.Error(w, "borrado no disponible", http.StatusServiceUnavailable)
		return
	}

	var emailAddr string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT email FROM users WHERE id = ?`, s.UserID,
	).Scan(&emailAddr); err != nil {
		h.logger.Error("delete: cargar email", "err", err, "user_id", s.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	code, err := generateDeletionCode()
	if err != nil {
		h.logger.Error("delete: generar código", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.storeDeletionCode(r.Context(), s.UserID, code); err != nil {
		h.logger.Error("delete: store code", "err", err, "user_id", s.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.sendDeletionEmail(emailAddr, code); err != nil {
		h.logger.Error("delete: enviar email", "err", err, "user_id", s.UserID)
		http.Error(w, "no pudimos mandar el email; intentá de nuevo", http.StatusBadGateway)
		return
	}
	h.logger.Info("delete: código enviado", "user_id", s.UserID)
	http.Redirect(w, r, "/settings/delete-account?sent=1", http.StatusSeeOther)
}

// deleteAccountConfirm valida el código y marca deleted_at. También destruye
// las sesiones del user — incluyendo la actual; la próxima request del
// browser ya no tendrá sesión, y un nuevo login lo mandará a /restore-account.
func (h *Handler) deleteAccountConfirm(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Sanitizamos a solo dígitos (mismo motivo que /auth/verify: clients de
	// email meten non-breaking spaces al copiar "123 456").
	code := sanitizeDeletionCode(r.PostFormValue("code"))
	if !isValidDeletionCode(code) {
		h.renderDeleteWithError(w, r, s, "código inválido")
		return
	}

	valid, err := h.consumeDeletionCode(r.Context(), s.UserID, code)
	if err != nil {
		h.logger.Error("delete: consume code", "err", err, "user_id", s.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !valid {
		h.renderDeleteWithError(w, r, s, "código incorrecto o expirado")
		return
	}

	now := time.Now().UTC().Unix()
	if _, err := h.db.ExecContext(r.Context(),
		`UPDATE users SET deleted_at = ? WHERE id = ?`, now, s.UserID,
	); err != nil {
		h.logger.Error("delete: marcar deleted_at", "err", err, "user_id", s.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM sessions WHERE user_id = ?`, s.UserID,
	); err != nil {
		h.logger.Error("delete: destruir sesiones", "err", err, "user_id", s.UserID)
		// No abortamos: el deleted_at ya está marcado, el flow sigue.
	}
	h.logger.Info("delete: cuenta marcada para borrar", "user_id", s.UserID)
	http.Redirect(w, r, "/?deleted=1", http.StatusSeeOther)
}

// renderDeleteWithError re-renderiza /settings/delete-account con un error
// inline (después de un POST que falló validación).
func (h *Handler) renderDeleteWithError(w http.ResponseWriter, r *http.Request, s *session.Session, msg string) {
	var emailAddr string
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT email FROM users WHERE id = ?`, s.UserID,
	).Scan(&emailAddr)
	w.WriteHeader(http.StatusBadRequest)
	h.render(w, "delete-account.html", deleteAccountData{
		Session:  s,
		Email:    emailAddr,
		CodeSent: true,
		Error:    msg,
	})
}

// ---- restore (cuenta soft-deleted vuelve a loguear) ----

type restoreAccountData struct {
	Session         *session.Session
	Email           string
	DeletedAt       time.Time
	HardDeleteAt    time.Time
	HardDeleteIn    string // texto humano: "en 5 días"
}

// restoreAccountPage muestra la página de restauración. Requiere sesión con
// user que tiene deleted_at != NULL (lo enforce el middleware de server).
func (h *Handler) restoreAccountPage(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	var (
		emailAddr string
		deletedAt sql.NullInt64
	)
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT email, deleted_at FROM users WHERE id = ?`, s.UserID,
	).Scan(&emailAddr, &deletedAt); err != nil {
		h.logger.Error("restore: cargar user", "err", err, "user_id", s.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !deletedAt.Valid {
		// No está pendiente de borrado — vuelta a la home.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	deletedTime := time.Unix(deletedAt.Int64, 0).UTC()
	hardDelete := deletedTime.Add(GracePeriod)
	h.render(w, "restore-account.html", restoreAccountData{
		Session:      s,
		Email:        emailAddr,
		DeletedAt:    deletedTime,
		HardDeleteAt: hardDelete,
		HardDeleteIn: humanDuration(time.Until(hardDelete)),
	})
}

// restoreAccountCancel limpia deleted_at — el user vuelve a su cuenta normal.
func (h *Handler) restoreAccountCancel(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		`UPDATE users SET deleted_at = NULL WHERE id = ?`, s.UserID,
	); err != nil {
		h.logger.Error("restore: cancelar borrado", "err", err, "user_id", s.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.logger.Info("restore: borrado cancelado", "user_id", s.UserID)
	http.Redirect(w, r, "/library", http.StatusSeeOther)
}

// restoreAccountDeleteNow saltea el período de gracia y fuerza hard delete
// inmediato. Sigue la misma lógica del worker (ver cleanup_worker.go).
func (h *Handler) restoreAccountDeleteNow(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if err := HardDeleteUser(r.Context(), h.db, s.UserID, deletionLogPepperOrEmpty()); err != nil {
		h.logger.Error("restore: hard delete inmediato", "err", err, "user_id", s.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.logger.Info("restore: hard delete inmediato confirmado", "user_id", s.UserID)
	http.Redirect(w, r, "/?deleted=1", http.StatusSeeOther)
}

// deletionLogPepperOrEmpty existe para que profile no acople con env vars
// directas — el pepper se inyecta vía un setter al construir el Handler.
// Si el setter no se llamó (tests), el hash usa string vacío como pepper:
// inseguro fuera de tests, pero suficiente.
func deletionLogPepperOrEmpty() string { return globalDeletionLogPepper }

// globalDeletionLogPepper es seteado por SetDeletionLogPepper desde main.go.
var globalDeletionLogPepper string

// SetDeletionLogPepper inyecta el pepper para el hash del audit log. Se llama
// una vez al arrancar el servidor.
func SetDeletionLogPepper(p string) { globalDeletionLogPepper = p }

// ---- helpers OTP-locales ----

func generateDeletionCode() (string, error) {
	var code [deletionCodeLength]byte
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		code[i] = byte('0' + n.Int64())
	}
	return string(code[:]), nil
}

func isValidDeletionCode(s string) bool {
	if len(s) != deletionCodeLength {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sanitizeDeletionCode extrae solo dígitos (cubre non-breaking spaces y otros
// invisibles que algunos clients de email inyectan al pegar el código).
func sanitizeDeletionCode(s string) string {
	out := make([]byte, 0, deletionCodeLength)
	for _, r := range s {
		if r >= '0' && r <= '9' {
			out = append(out, byte(r))
		}
	}
	return string(out)
}

func hashDeletionCode(c string) string {
	sum := sha256.Sum256([]byte(c))
	return hex.EncodeToString(sum[:])
}

// storeDeletionCode inserta una fila nueva; no borramos códigos previos,
// solo expiran. El consume rechaza los expirados.
func (h *Handler) storeDeletionCode(ctx context.Context, userID int64, code string) error {
	now := time.Now().UTC()
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO deletion_codes (user_id, code_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?)
	`, userID, hashDeletionCode(code), now.Unix(), now.Add(deletionCodeDuration).Unix())
	return err
}

// consumeDeletionCode busca el código (no expirado), lo borra (single-use)
// y retorna true si existía.
func (h *Handler) consumeDeletionCode(ctx context.Context, userID int64, code string) (bool, error) {
	res, err := h.db.ExecContext(ctx, `
		DELETE FROM deletion_codes
		WHERE user_id = ? AND code_hash = ? AND expires_at > ?
	`, userID, hashDeletionCode(code), time.Now().UTC().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (h *Handler) sendDeletionEmail(to, code string) error {
	var buf bytes.Buffer
	if err := h.deleteT.Execute(&buf, map[string]any{
		"Code":    code,
		"BaseURL": h.baseURL,
	}); err != nil {
		return err
	}
	return h.email.Send(to, "confirmá el borrado de tu cuenta mokuji", buf.String())
}

// ---- humanDuration ----

func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "ahora"
	}
	days := int(d.Hours() / 24)
	if days >= 1 {
		if days == 1 {
			return "en 1 día"
		}
		return "en " + itoa(days) + " días"
	}
	hours := int(d.Hours())
	if hours >= 1 {
		if hours == 1 {
			return "en 1 hora"
		}
		return "en " + itoa(hours) + " horas"
	}
	return "en menos de una hora"
}

// itoa minimal helper para no importar strconv en este archivo.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

// Hint al linter de que email no está sin uso: la referencia mantiene el
// import vivo cuando agreguemos features que usen Message directo.
var _ = email.Message{}

// ErrUserNotPendingDeletion lo devuelve HardDeleteUser cuando el user
// existe pero no está soft-deleted. Útil para el worker.
var ErrUserNotPendingDeletion = errors.New("user no está marcado para borrar")
