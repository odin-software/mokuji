package profile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Reglas del slug. Cambios acá implican migración cuidadosa de los slugs
// existentes — no relajar sin revisar usuarios actuales.
const (
	usernameMinLen = 3
	usernameMaxLen = 20

	// UsernameCooldown limita la frecuencia con la que un user puede
	// cambiar su slug. Tras un cambio, hay que esperar este tiempo antes
	// de cambiar de nuevo. El slug viejo queda reservado el mismo lapso
	// para que nadie más lo agarre y suplante.
	UsernameCooldown = 30 * 24 * time.Hour
)

// usernameRe acepta minúsculas, dígitos, guión y guión bajo. Inicio y fin
// con letra o dígito (no separadores). Esto bloquea cosas como "-foo",
// "foo-", "__bar__", y normaliza a un namespace estable.
var usernameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`)

// usernameDigitsOnlyRe bloquea slugs puramente numéricos: colisionan con
// el routing legacy /u/{id} (ambiguedad slug vs id). Si el slug fuera
// "12345", no podríamos distinguirlo de un user con id=12345.
var usernameDigitsOnlyRe = regexp.MustCompile(`^[0-9]+$`)

// reservedUsernames son slugs prohibidos porque colisionan con rutas de
// la app o nombres genéricos que no queremos en manos de un user random.
// Comparación case-insensitive vía normalización a lowercase antes del
// lookup en el set.
var reservedUsernames = map[string]struct{}{
	// Rutas top-level que existen o pueden existir pronto.
	"admin":               {},
	"api":                 {},
	"auth":                {},
	"library":             {},
	"releases":            {},
	"settings":            {},
	"unsubscribe":         {},
	"restore-account":     {},
	"static":              {},
	"version":             {},
	"manifest.webmanifest": {},
	"sw.js":               {},
	"r":                   {}, // shortcut del digest /r/{token}
	"u":                   {}, // namespace propio de perfiles
	// Identidades genéricas / marca.
	"mokuji": {},
	"me":     {},
	"home":   {},
	"index":  {},
	"profile": {},
	"www":    {},
	"root":   {},
}

// Errores de validación reportables al user. El handler los traduce a
// mensajes en español para la UI; mantenemos los códigos en inglés para
// no acoplar dominio con presentación.
var (
	ErrUsernameTooShort   = errors.New("username demasiado corto")
	ErrUsernameTooLong    = errors.New("username demasiado largo")
	ErrUsernameInvalid    = errors.New("username con caracteres no permitidos")
	ErrUsernameDigitsOnly = errors.New("username no puede ser solo números")
	ErrUsernameReserved   = errors.New("username reservado")
	ErrUsernameTaken      = errors.New("username ya tomado")
	ErrUsernameCooldown   = errors.New("username cambiado hace muy poco")
)

// ValidateUsername normaliza (lowercase + trim) y aplica las reglas de
// formato. Devuelve el slug normalizado listo para guardar o un error
// específico. No consulta DB — eso lo hace el caller con upsertUsername.
func ValidateUsername(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if len(s) < usernameMinLen {
		return "", ErrUsernameTooShort
	}
	if len(s) > usernameMaxLen {
		return "", ErrUsernameTooLong
	}
	if !usernameRe.MatchString(s) {
		return "", ErrUsernameInvalid
	}
	if usernameDigitsOnlyRe.MatchString(s) {
		return "", ErrUsernameDigitsOnly
	}
	if _, reserved := reservedUsernames[s]; reserved {
		return "", ErrUsernameReserved
	}
	return s, nil
}

// upsertUsername setea o cambia el slug del user. Lógica:
//
//   - Si el user no tenía slug → INSERT directo (sin cooldown).
//   - Si el user tenía slug → enforce cooldown (UsernameCooldown desde el
//     último cambio). Si pasa el chequeo, mueve el viejo a previous_slug
//     con una expiración para reservarlo.
//   - En cualquier caso, valida que el slug nuevo no esté tomado por otro
//     user (en slug o en previous_slug activo).
//
// El check de "tomado" + UPDATE corre en una transacción para evitar
// race entre dos users que mandan POST simultáneo. UNIQUE indexes
// parciales dan el último candado de seguridad si la transacción no
// alcanza.
func (h *Handler) upsertUsername(ctx context.Context, userID int64, newSlug string, now time.Time) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Cargar estado actual del user (slug + slug_changed_at). El SELECT en
	// la misma tx asegura que el snapshot es consistente con el UPDATE.
	var currentSlug sql.NullString
	var changedAt sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT slug, slug_changed_at FROM users WHERE id = ?`, userID,
	).Scan(&currentSlug, &changedAt); err != nil {
		return fmt.Errorf("cargar user: %w", err)
	}

	// Cooldown solo aplica si ya había slug y está cambiando. Setear el
	// mismo slug que ya tienes es no-op (silencioso).
	if currentSlug.Valid && currentSlug.String == newSlug {
		return nil
	}
	if currentSlug.Valid && changedAt.Valid {
		nextAllowed := time.Unix(changedAt.Int64, 0).Add(UsernameCooldown)
		if now.Before(nextAllowed) {
			return ErrUsernameCooldown
		}
	}

	// Check de unicidad: rechazamos si otro user tiene este slug como
	// actual o como previous_slug aún reservado. Los UNIQUE indexes ya lo
	// harían, pero el chequeo previo nos permite devolver un error
	// específico (ErrUsernameTaken) en lugar de descifrar un constraint.
	var taken int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM users
		  WHERE id != ?
		    AND (slug = ? COLLATE NOCASE
		         OR (previous_slug = ? COLLATE NOCASE
		             AND previous_slug_until > ?))
		)`, userID, newSlug, newSlug, now.Unix()).Scan(&taken); err != nil {
		return fmt.Errorf("chequear taken: %w", err)
	}
	if taken == 1 {
		return ErrUsernameTaken
	}

	// Si había slug previo, lo movemos a previous_slug con expiración.
	// Si no había, solo INSERT el nuevo. Limpiamos cualquier reservation
	// expirada del propio user (caso edge: cambió hace 60 días, el viejo
	// ya está libre pero la fila aún tiene previous_slug; sobrescribimos
	// con el current al moverlo).
	var prevSlug sql.NullString
	var prevUntil sql.NullInt64
	if currentSlug.Valid {
		prevSlug = sql.NullString{String: currentSlug.String, Valid: true}
		prevUntil = sql.NullInt64{Int64: now.Add(UsernameCooldown).Unix(), Valid: true}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE users
		SET slug = ?,
		    slug_changed_at = ?,
		    previous_slug = ?,
		    previous_slug_until = ?
		WHERE id = ?
	`, newSlug, now.Unix(), prevSlug, prevUntil, userID); err != nil {
		// El UNIQUE index puede saltar acá si hubo race con otro user
		// (entre el SELECT EXISTS y el UPDATE). Traducimos a "tomado"
		// para el caller.
		if isUniqueViolation(err) {
			return ErrUsernameTaken
		}
		return fmt.Errorf("update users: %w", err)
	}

	return tx.Commit()
}

// lookupUserBySlug busca un user por su slug actual (case-insensitive).
// NO mira previous_slug — esos están reservados pero no activos como
// rutas accesibles. Devuelve sql.ErrNoRows si no hay match.
func (h *Handler) lookupUserBySlug(ctx context.Context, slug string) (int64, error) {
	var id int64
	err := h.db.QueryRowContext(ctx,
		`SELECT id FROM users WHERE slug = ? COLLATE NOCASE AND deleted_at IS NULL`,
		slug,
	).Scan(&id)
	return id, err
}

// loadUserSlug devuelve el slug actual del user (o NullString.Valid=false
// si no tiene) y el unix de `slug_changed_at` (para mostrar cuándo podrá
// cambiar de nuevo en la UI).
func (h *Handler) loadUserSlug(ctx context.Context, userID int64) (sql.NullString, sql.NullInt64, error) {
	var slug sql.NullString
	var changed sql.NullInt64
	err := h.db.QueryRowContext(ctx,
		`SELECT slug, slug_changed_at FROM users WHERE id = ?`, userID,
	).Scan(&slug, &changed)
	return slug, changed, err
}

// isUniqueViolation detecta los errores del driver SQLite (modernc) para
// constraint UNIQUE. modernc devuelve errores con el texto "UNIQUE
// constraint failed". Match por string es feo pero estable — el driver
// no expone códigos tipados.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
