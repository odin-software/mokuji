// Package grant emite y canjea tokens de un solo uso para recuperación de
// acceso administrativa (break-glass).
//
// Existe porque el flow de auth de mokuji depende enteramente de email: si
// el envío se cae — key revocada, provider suspendido, quota agotada — no
// hay forma de entrar. Un grant rompe esa dependencia sin abrir una segunda
// vía de auth permanente: lo emite POST /admin/grant-session detrás de
// ADMIN_TOKEN, y lo canjea GET /auth/grant una sola vez.
//
// Vive en su propio paquete porque lo consumen dos lados: internal/admin
// emite e internal/auth canjea. Ponerlo en cualquiera de los dos ataría un
// paquete al otro sin razón.
package grant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// TTL es la ventana de validez de un grant. Corta a propósito: el token
// viaja en query string y termina en los access logs de nginx, así que la
// exposición se acota con tiempo, no con secreto.
const TTL = 5 * time.Minute

// ErrInvalid cubre token inexistente, ya usado y vencido — los tres con el
// mismo error. Distinguirlos le daría a quien pruebe tokens una señal sobre
// cuáles existieron alguna vez.
var ErrInvalid = errors.New("grant inválido")

// Issue crea un grant para userID y retorna el token en plaintext. El
// plaintext solo existe en este return: en DB queda únicamente su SHA-256.
//
// No valida que userID exista — de eso se encarga el caller, que además
// necesita distinguir "user inexistente" para responder 404. La FK de la
// tabla es la red de seguridad.
func Issue(ctx context.Context, db *sql.DB, userID int64) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)

	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO admin_grants (token_hash, user_id, created_at, expires_at)
		VALUES (?, ?, ?, ?)
	`, hashToken(token), userID, now.Unix(), now.Add(TTL).Unix()); err != nil {
		return "", err
	}
	return token, nil
}

// Consume canjea el token y retorna el user_id asociado. Retorna ErrInvalid
// si no existe, ya se usó o venció.
//
// El UPDATE condicional con RETURNING hace validación y consumo en una sola
// operación atómica: dos requests con el mismo token no pueden ganar ambos,
// porque el segundo ya no matchea used_at IS NULL.
func Consume(ctx context.Context, db *sql.DB, token string) (int64, error) {
	now := time.Now().UTC().Unix()
	var userID int64
	err := db.QueryRowContext(ctx, `
		UPDATE admin_grants
		SET used_at = ?
		WHERE token_hash = ? AND expires_at > ? AND used_at IS NULL
		RETURNING user_id
	`, now, hashToken(token), now).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrInvalid
	}
	if err != nil {
		return 0, err
	}
	return userID, nil
}

// CleanupExpired borra los grants vencidos. Sin retención extra: una vez
// pasado expires_at no hay nada que consultar, ni siquiera para auditoría
// — el uso queda en los logs, no en la tabla.
func CleanupExpired(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM admin_grants WHERE expires_at < ?`,
		time.Now().UTC().Unix(),
	)
	return err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
