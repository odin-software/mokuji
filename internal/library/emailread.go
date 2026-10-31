package library

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/odin-software/mokuji/internal/unsubscribe"
)

// GET /r/{token} es el "shortcut" del email digest: el link de cada cap en
// el body apunta acá en vez de directamente al lector externo. Token =
// HMAC(userID:chapterID:scope:issuedAt) firmado con UNSUBSCRIBE_SECRET y
// validez 30 días.
//
// El handler:
//   1. valida el token (scope + firma + edad)
//   2. busca el chapter y verifica que el user lo tenga en su library con
//      status=reading y manga matcheado (la cadena de joins falla en
//      cualquier otro caso, devolviendo 0 rows -> "link inválido")
//   3. avanza el cursor del library_entry
//   4. responde con meta refresh al external_url del cap (mismo patrón que
//      entryReadAndOpen, para evitar la CSP form-action en redirects
//      cross-origin — acá no aplica porque es GET, pero usar el mismo
//      mecanismo es consistente y simpler de mantener)
//
// El endpoint no requiere sesión: el token ES la auth. Si el user está
// soft-deleted, el match con library_entries activo no produce filas y el
// handler responde "link inválido" — el user vería el flow de restore al
// volver a la app, no acá.

func (h *Handler) emailRead(w http.ResponseWriter, r *http.Request) {
	if h.signer == nil {
		http.NotFound(w, r)
		return
	}
	token := r.PathValue("token")
	if token == "" {
		http.NotFound(w, r)
		return
	}

	userID, chapterID, err := h.signer.VerifyReadChapter(token)
	if err != nil {
		h.renderEmailReadError(w, err)
		return
	}

	external, err := h.markFromEmail(r.Context(), userID, chapterID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Cap no está en una library activa del user (o user borrado).
			// No revelamos cuál de los casos para no leak.
			h.renderEmailReadError(w, unsubscribe.ErrInvalidToken)
			return
		}
		h.logger.Error("email read: mark", "user_id", userID, "chapter_id", chapterID, "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	writeRedirectPage(w, external)
}

// markFromEmail combina la validación de ownership + el mark-read + la
// carga del external_url en una sola query SQL para minimizar round-trips
// y evitar TOCTOU. Devuelve sql.ErrNoRows si el cap no pertenece a una
// library_entry activa (status='reading') del user, o si el user está
// soft-deleted, o si no hay match con mangadex.
func (h *Handler) markFromEmail(ctx context.Context, userID, chapterID int64) (string, error) {
	// Cargamos external_url + source_id + entry_id + chapter number en
	// una query con joins que aseguran ownership. Si la cadena rompe en
	// cualquier lado, no devuelve filas.
	var (
		entryID     int64
		sourceID    string
		externalURL sql.NullString
		chapterNum  string
	)
	err := h.db.QueryRowContext(ctx, `
		SELECT le.id, c.source_id, c.external_url, c.number
		FROM chapter c
		JOIN library_entries le ON le.manga_id = c.manga_id
		JOIN users u ON u.id = le.user_id
		WHERE c.id = ?
		  AND le.user_id = ?
		  AND le.status = 'reading'
		  AND u.deleted_at IS NULL
	`, chapterID, userID).Scan(&entryID, &sourceID, &externalURL, &chapterNum)
	if err != nil {
		return "", err
	}

	// Avanzamos el cursor al MAX(cursor_actual, número del cap). No
	// fallamos si el UPDATE no afecta filas (race con un delete) — lo
	// importante es responder al link, no marcar.
	_, err = h.db.ExecContext(ctx, `
		UPDATE library_entries
		SET last_read_chapter_number = MAX(
		      COALESCE(last_read_chapter_number, 0),
		      CAST(? AS REAL)
		    ),
		    updated_at = ?
		WHERE id = ?
	`, chapterNum, time.Now().UTC().Unix(), entryID)
	if err != nil {
		return "", err
	}

	if externalURL.Valid && externalURL.String != "" {
		return externalURL.String, nil
	}
	return "https://mangadex.org/chapter/" + sourceID, nil
}

// renderEmailReadError muestra un mensaje breve cuando el token es inválido
// o expiró. Sin sesión necesaria — el link viene de un email.
func (h *Handler) renderEmailReadError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusGone)
	msg := "este link de email expiró o ya no es válido."
	if errors.Is(err, unsubscribe.ErrExpiredToken) {
		msg = "este link de email expiró (los links del digest duran 30 días). abre la app para leer el cap directo."
	}
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html lang="es"><head><meta charset="utf-8"><title>link expirado — mokuji</title></head>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 3rem auto; padding: 0 1rem; line-height: 1.6;">
<p>` + msg + `</p>
<p><a href="/library">→ ir a mi biblioteca</a></p>
</body></html>`))
}
