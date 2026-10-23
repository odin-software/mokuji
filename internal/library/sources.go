package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"git.odin.do/kengru/mokuji/internal/releases/matcher"
	"git.odin.do/kengru/mokuji/internal/session"
)

// EntrySource es una fila renderizada del fieldset "sources" del detail
// page. Refleja una source — sea una fila real en manga_sources, o una
// source configurada que todavía no fue intentada.
//
// HasRow distingue los dos casos: con fila se puede desvincular y
// reintentar; sin fila solo se puede intentar. Configured marca si el
// handler tiene matcher cableado para esa source — sources con fila
// pero deshabilitadas (env apagada) se muestran en gris sin acciones.
type EntrySource struct {
	Source      string
	SourceID    string
	Status      matcher.Status
	CheckedAt   time.Time
	NextCheckAt time.Time
	HasRow      bool
	Configured  bool
	// State es lo que se le muestra al user (ver sourceState) y Chapters
	// cuántos capítulos distintos trajo esta source.
	State    SourceState
	Chapters int
}

// loadEntrySources arma la lista renderizable para el fieldset combinando
// las filas existentes de manga_sources con las sources que el handler
// tiene cableadas pero no han sido intentadas todavía (escenario: user
// agregó el manga antes de habilitar Asura). El orden es estable
// alfabético por nombre de source.
func (h *Handler) loadEntrySources(ctx context.Context, mangaID int64) ([]EntrySource, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT ms.source, ms.source_id, ms.status, ms.checked_at, ms.next_check_at,
		       ms.consecutive_failures, COUNT(DISTINCT c.number) AS chapter_count
		FROM manga_sources ms
		LEFT JOIN chapter c ON c.manga_id = ms.manga_id AND c.source = ms.source
		WHERE ms.manga_id = ?
		GROUP BY ms.source, ms.source_id, ms.status, ms.checked_at, ms.next_check_at, ms.consecutive_failures
	`, mangaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byName := map[string]EntrySource{}
	for rows.Next() {
		var (
			es           EntrySource
			checked      sql.NullInt64
			nextCheck    sql.NullInt64
			failures     int
		)
		if err := rows.Scan(&es.Source, &es.SourceID, &es.Status, &checked, &nextCheck, &failures, &es.Chapters); err != nil {
			return nil, err
		}
		es.HasRow = true
		_, es.Configured = h.matchers[es.Source]
		if checked.Valid {
			es.CheckedAt = time.Unix(checked.Int64, 0).UTC()
		}
		if nextCheck.Valid {
			es.NextCheckAt = time.Unix(nextCheck.Int64, 0).UTC()
		}
		es.State = sourceState(es.Status, nextCheck.Valid, es.Chapters, failures)
		byName[es.Source] = es
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Agregar entries placeholder para sources configuradas sin fila —
	// permite al user disparar el primer matching desde la UI.
	for name := range h.matchers {
		if _, ok := byName[name]; ok {
			continue
		}
		byName[name] = EntrySource{
			Source:     name,
			Status:     matcher.StatusUnmatched,
			Configured: true,
			State:      SourceSearching,
		}
	}

	// Orden estable: alfabético por nombre. Es N≤4 sources en práctica,
	// no vale la pena un orden más sofisticado (popularidad, etc.).
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	out := make([]EntrySource, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out, nil
}

// entrySourceRetry sirve POST /library/entry/{id}/sources/{source}/retry.
//
// Re-corre el matcher de esa source contra el título del manga y
// upserta la fila resultante en manga_sources. Útil cuando la source
// rotó dominio, agregó el manga, o la primera tentativa falló por red.
//
// No dispara poll inmediato si matchea — el worker lo retomará en su
// próximo tick (next_check_at queda NULL tras el upsert). Eso evita
// que un click en "reintentar" se quede colgado esperando una API
// lenta.
func (h *Handler) entrySourceRetry(w http.ResponseWriter, r *http.Request) {
	s, entry, sourceName, ok := h.parseSourceForm(w, r)
	if !ok {
		return
	}
	m, ok := h.matchers[sourceName]
	if !ok {
		http.Error(w, "source no configurada", http.StatusBadRequest)
		return
	}

	result, err := m.Match(r.Context(), entry.Manga.Title)
	if err != nil {
		// Errores del matcher (rate limit, red caída) los logueamos
		// pero no devolvemos 500: el user sabe que reintenta y un
		// fallo es esperable. Le mostramos el detail page de vuelta.
		h.logger.Warn("entry source retry: matcher",
			"user_id", s.UserID, "manga_id", entry.Manga.ID, "source", sourceName, "err", err)
		http.Redirect(w, r, fmt.Sprintf("/library/entry/%d", entry.ID), http.StatusSeeOther)
		return
	}

	var sourceID string
	if result.Status == matcher.StatusMatched {
		sourceID = result.SourceID
	}
	if err := h.upsertMangaSource(r.Context(), entry.Manga.ID, sourceName, string(result.Status), sourceID); err != nil {
		h.logger.Error("entry source retry: upsert",
			"manga_id", entry.Manga.ID, "source", sourceName, "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/library/entry/%d", entry.ID), http.StatusSeeOther)
}

// entrySourceUnlink sirve POST /library/entry/{id}/sources/{source}/unlink.
//
// Borra la fila de manga_sources — el worker deja de polleear esa
// (manga, source). Los capítulos ya persistidos quedan; solo dejamos
// de buscar nuevos. Idempotente: si la fila no existe ya, devolvemos
// igualmente al detail.
func (h *Handler) entrySourceUnlink(w http.ResponseWriter, r *http.Request) {
	_, entry, sourceName, ok := h.parseSourceForm(w, r)
	if !ok {
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM manga_sources WHERE manga_id = ? AND source = ?`,
		entry.Manga.ID, sourceName,
	); err != nil {
		h.logger.Error("entry source unlink", "manga_id", entry.Manga.ID, "source", sourceName, "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/library/entry/%d", entry.ID), http.StatusSeeOther)
}

// parseSourceForm valida sesión, ownership del entry, y extrae el nombre
// de source desde el path. El nombre se valida contra letras/guiones
// minúsculos para que no se inyecte SQL via parámetro (defensa en
// profundidad — los SQLs ya usan parámetros).
func (h *Handler) parseSourceForm(w http.ResponseWriter, r *http.Request) (s *session.Session, entry *Entry, sourceName string, ok bool) {
	s, sess := h.requireSession(w, r)
	if !sess {
		return nil, nil, "", false
	}
	entryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || entryID <= 0 {
		http.NotFound(w, r)
		return nil, nil, "", false
	}
	loaded, exists, err := h.loadEntry(r.Context(), s.UserID, entryID)
	if err != nil {
		h.logger.Error("parse source form: load entry", "err", err, "entry_id", entryID)
		h.errs.InternalServerError(w, r)
		return nil, nil, "", false
	}
	if !exists {
		http.NotFound(w, r)
		return nil, nil, "", false
	}
	sourceName = r.PathValue("source")
	if !isValidSourceName(sourceName) {
		http.Error(w, "source inválida", http.StatusBadRequest)
		return nil, nil, "", false
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, nil, "", false
	}
	return s, &loaded, sourceName, true
}

// isValidSourceName chequea que sourceName matchee [a-z][a-z0-9-]{0,30}.
// Sources en la práctica son palabras cortas en lowercase ("mangadex",
// "asura", "comick"). Validar acá evita strings raros en logs y blinda
// la subquery por si en el futuro se usa interpolación.
func isValidSourceName(s string) bool {
	if len(s) == 0 || len(s) > 31 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && (r >= '0' && r <= '9' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// ErrSourceNotConfigured se devuelve cuando se intenta operar sobre una
// source que no tiene matcher cableado. No se usa en el flow del handler
// directamente (el handler responde 400) pero existe para casos donde
// loadEntrySources detecta una row con source desconocida.
var ErrSourceNotConfigured = errors.New("source no configurada")
