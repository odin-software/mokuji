// Package session implementa sessions server-side con almacenamiento en
// SQLite (ADR 0005).
//
// Diseño:
//   - Session ID es 256 bits de crypto/rand, codificado en base64url
//   - En DB se guarda el SHA-256 hex del ID (defense-in-depth contra leak)
//   - Sliding window de 90 días; last_used_at se actualiza cada hora máximo
//   - Cookie con Secure (en producción), HttpOnly, SameSite=Lax
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

const (
	cookieName      = "mokuji_session"
	cookiePath      = "/"
	idleDuration    = 90 * 24 * time.Hour
	refreshInterval = 1 * time.Hour
)

// Session representa una sesión activa cargada desde DB.
type Session struct {
	IDHash     string // SHA-256 hex del ID; nunca el plaintext
	UserID     int64
	CSRFToken  string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
}

// Manager administra el ciclo de vida de sessions.
type Manager struct {
	db     *sql.DB
	secure bool
	logger *slog.Logger
}

// New construye un Manager. secure=true para producción con HTTPS;
// secure=false para dev local sin HTTPS.
func New(db *sql.DB, secure bool, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{db: db, secure: secure, logger: logger}
}

// Create genera una sesión para userID, guarda la fila en DB y setea la
// cookie de sesión en la response.
func (m *Manager) Create(w http.ResponseWriter, r *http.Request, userID int64) error {
	id, err := generateToken()
	if err != nil {
		return err
	}
	csrf, err := generateToken()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	expires := now.Add(idleDuration)

	_, err = m.db.ExecContext(r.Context(), `
		INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, last_used_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, hashID(id), userID, csrf, now.Unix(), now.Unix(), expires.Unix())
	if err != nil {
		return err
	}

	m.setCookie(w, id, expires)
	return nil
}

// Load lee la cookie, valida la fila en DB, y retorna la Session si es
// válida. Retorna (nil, nil) si no hay cookie o si la sesión no existe o
// expiró. Si pasó refreshInterval desde el último uso, actualiza la ventana.
func (m *Manager) Load(r *http.Request) (*Session, error) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}

	hash := hashID(c.Value)
	var s Session
	var created, lastUsed, expires int64
	err = m.db.QueryRowContext(r.Context(), `
		SELECT id_hash, user_id, csrf_token, created_at, last_used_at, expires_at
		FROM sessions WHERE id_hash = ?
	`, hash).Scan(&s.IDHash, &s.UserID, &s.CSRFToken, &created, &lastUsed, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.CreatedAt = time.Unix(created, 0).UTC()
	s.LastUsedAt = time.Unix(lastUsed, 0).UTC()
	s.ExpiresAt = time.Unix(expires, 0).UTC()

	now := time.Now().UTC()
	if now.After(s.ExpiresAt) {
		_, _ = m.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE id_hash = ?`, hash)
		return nil, nil
	}

	if now.Sub(s.LastUsedAt) > refreshInterval {
		newExpiry := now.Add(idleDuration)
		_, _ = m.db.ExecContext(r.Context(),
			`UPDATE sessions SET last_used_at = ?, expires_at = ? WHERE id_hash = ?`,
			now.Unix(), newExpiry.Unix(), hash,
		)
		s.LastUsedAt = now
		s.ExpiresAt = newExpiry
	}

	return &s, nil
}

// Destroy invalida la sesión actual: borra la fila en DB y limpia la cookie.
func (m *Manager) Destroy(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		if _, err := m.db.ExecContext(r.Context(),
			`DELETE FROM sessions WHERE id_hash = ?`, hashID(c.Value),
		); err != nil {
			return err
		}
	}
	m.clearCookie(w)
	return nil
}

// CleanupExpired borra sesiones cuya expires_at ya pasó. Llamar
// periódicamente desde una goroutine con ticker.
func (m *Manager) CleanupExpired(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix(),
	)
	return err
}

func (m *Manager) setCookie(w http.ResponseWriter, id string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    id,
		Path:     cookiePath,
		Expires:  expires,
		MaxAge:   int(idleDuration.Seconds()),
		Secure:   m.secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (m *Manager) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     cookiePath,
		MaxAge:   -1,
		Secure:   m.secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}
