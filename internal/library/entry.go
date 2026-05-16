package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"git.odin.do/kengru/mokuji/internal/session"
)

// Página detail de un manga: header con info + lista cronológica de capítulos.
// Sort: published_at DESC default, ASC opcional (?sort=asc).
// Include read: por default oculta caps anteriores al cursor; con ?include_read=1
// los muestra como leídos.
//
// Autorización: el endpoint scope estricto por user_id. Si el library_entry
// no existe para el user, 404 (sin revelar existencia del manga).

// Chapter es una fila de la lista de caps en la vista detail.
type Chapter struct {
	ID          int64
	Number      string
	Title       string
	Language    string
	ExternalURL string
	PublishedAt time.Time
	Read        bool // true si este cap está en o antes del cursor
}

type entryData struct {
	Session       *session.Session
	Entry         Entry
	Chapters      []Chapter
	Sort          string // "asc" | "desc"
	IncludeRead   bool
	UnreadCount   int
	TotalChapters int
}

// entryDetail sirve GET /library/entry/{id}.
func (h *Handler) entryDetail(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	entryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || entryID <= 0 {
		http.NotFound(w, r)
		return
	}

	entry, ok, err := h.loadEntry(r.Context(), s.UserID, entryID)
	if err != nil {
		h.logger.Error("entry detail: cargar entry", "err", err, "entry_id", entryID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		// El entry no es del user logueado. 404 sin revelar.
		http.NotFound(w, r)
		return
	}

	sort := r.URL.Query().Get("sort")
	if sort != "asc" {
		sort = "desc"
	}
	includeRead := r.URL.Query().Get("include_read") == "1"

	chapters, err := h.loadChaptersForEntry(r.Context(), s.UserID, entry.Manga.ID, sort, includeRead)
	if err != nil {
		h.logger.Error("entry detail: cargar chapters", "err", err, "entry_id", entryID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.render(w, "library/entry.html", entryData{
		Session:       s,
		Entry:         entry,
		Chapters:      chapters,
		Sort:          sort,
		IncludeRead:   includeRead,
		UnreadCount:   entry.UnreadCount,
		TotalChapters: len(chapters),
	})
}

// entryReadAndOpen sirve POST /library/entry/{id}/read.
// Mark-read del capítulo indicado + 303 redirect a su external_url para que
// el form con target="_blank" abra el lector en una pestaña nueva.
func (h *Handler) entryReadAndOpen(w http.ResponseWriter, r *http.Request) {
	s, entry, chapterID, ok := h.parseEntryReadForm(w, r)
	if !ok {
		return
	}

	if err := h.markChapterRead(r.Context(), s.UserID, entry.ID, chapterID); err != nil {
		h.logger.Error("read+open: mark", "err", err, "entry_id", entry.ID, "chapter_id", chapterID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	external, err := h.loadExternalURL(r.Context(), chapterID)
	if err != nil {
		h.logger.Error("read+open: external url", "err", err, "chapter_id", chapterID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, external, http.StatusSeeOther)
}

// entryMarkRead sirve POST /library/entry/{id}/mark-read.
// Mark-read sin redirect externo: vuelve al detail del manga.
func (h *Handler) entryMarkRead(w http.ResponseWriter, r *http.Request) {
	s, entry, chapterID, ok := h.parseEntryReadForm(w, r)
	if !ok {
		return
	}
	if err := h.markChapterRead(r.Context(), s.UserID, entry.ID, chapterID); err != nil {
		h.logger.Error("mark-read: mark", "err", err, "entry_id", entry.ID, "chapter_id", chapterID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/library/entry/%d", entry.ID), http.StatusSeeOther)
}

// parseEntryReadForm valida sesión + entry id de la URL + chapter_id del form
// + ownership del entry. Centraliza la guarda de los dos handlers POST.
func (h *Handler) parseEntryReadForm(w http.ResponseWriter, r *http.Request) (*session.Session, *Entry, int64, bool) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return nil, nil, 0, false
	}
	entryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || entryID <= 0 {
		http.NotFound(w, r)
		return nil, nil, 0, false
	}
	entry, exists, err := h.loadEntry(r.Context(), s.UserID, entryID)
	if err != nil {
		h.logger.Error("parse entry form: cargar entry", "err", err, "entry_id", entryID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, nil, 0, false
	}
	if !exists {
		http.NotFound(w, r)
		return nil, nil, 0, false
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, nil, 0, false
	}
	chapterID, err := strconv.ParseInt(r.PostFormValue("chapter_id"), 10, 64)
	if err != nil || chapterID <= 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, nil, 0, false
	}
	return s, &entry, chapterID, true
}

// loadEntry trae un library_entry + manga joineado, scoped al user. Segundo
// retorno = false si no existe (sin error).
func (h *Handler) loadEntry(ctx context.Context, userID, entryID int64) (Entry, bool, error) {
	var (
		e            Entry
		isFav        int
		lastCh       sql.NullString
		sourceURL    sql.NullString
		notes        sql.NullString
		coverURL     sql.NullString
		created, updated int64
	)
	err := h.db.QueryRowContext(ctx, `
		SELECT le.id, le.status, le.last_chapter, le.is_favorite, le.source_url, le.notes,
		       le.created_at, le.updated_at,
		       m.id, m.source, m.source_id, m.title, m.cover_url, m.mangadex_status,
		       (SELECT COUNT(*) FROM chapter c
		        WHERE c.manga_id = m.id AND c.language = u.preferred_language
		       ) AS total_chapters,
		       (SELECT COUNT(*) FROM chapter c
		        WHERE c.manga_id = m.id
		          AND c.language = u.preferred_language
		          AND (le.last_read_chapter_id IS NULL
		               OR c.published_at > (SELECT cur.published_at FROM chapter cur WHERE cur.id = le.last_read_chapter_id))
		       ) AS unread_count
		FROM library_entries le
		JOIN manga m ON m.id = le.manga_id
		JOIN users u ON u.id = le.user_id
		WHERE le.id = ? AND le.user_id = ?
	`, entryID, userID).Scan(
		&e.ID, &e.Status, &lastCh, &isFav, &sourceURL, &notes,
		&created, &updated,
		&e.Manga.ID, &e.Manga.Source, &e.Manga.SourceID, &e.Manga.Title, &coverURL,
		&e.MangadexStatus, &e.TotalChapters, &e.UnreadCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	e.IsFavorite = isFav == 1
	e.LastChapter = lastCh.String
	e.SourceURL = sourceURL.String
	e.Notes = notes.String
	e.Manga.CoverURL = coverURL.String
	e.CreatedAt = time.Unix(created, 0).UTC()
	e.UpdatedAt = time.Unix(updated, 0).UTC()
	return e, true, nil
}

// loadChaptersForEntry trae los caps del manga en el idioma preferido del user,
// con el flag Read precalculado contra el cursor. Si includeRead=false, filtra
// fuera los caps cuya published_at sea <= cursor.published_at.
func (h *Handler) loadChaptersForEntry(ctx context.Context, userID, mangaID int64, sort string, includeRead bool) ([]Chapter, error) {
	order := "DESC"
	if sort == "asc" {
		order = "ASC"
	}

	// El CASE genera el flag Read para que el template muestre el estado sin
	// queries adicionales. cur.published_at NULL = cursor sin setear = todos
	// unread.
	q := fmt.Sprintf(`
		SELECT c.id, c.number, c.title, c.language, c.source_id, c.external_url, c.published_at,
		       CASE
		         WHEN cur.published_at IS NULL THEN 0
		         WHEN c.published_at <= cur.published_at THEN 1
		         ELSE 0
		       END AS read_flag
		FROM chapter c
		JOIN manga m ON m.id = c.manga_id
		JOIN users u ON u.id = ?
		JOIN library_entries le ON le.user_id = u.id AND le.manga_id = m.id
		LEFT JOIN chapter cur ON cur.id = le.last_read_chapter_id
		WHERE m.id = ?
		  AND c.language = u.preferred_language
		  %s
		ORDER BY c.published_at %s
	`, includeReadFilter(includeRead), order)

	rows, err := h.db.QueryContext(ctx, q, userID, mangaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Chapter
	for rows.Next() {
		var (
			c           Chapter
			number      sql.NullString
			title       sql.NullString
			language    sql.NullString
			sourceID    string
			externalURL sql.NullString
			publishedAt sql.NullInt64
			readFlag    int
		)
		if err := rows.Scan(&c.ID, &number, &title, &language, &sourceID, &externalURL, &publishedAt, &readFlag); err != nil {
			return nil, err
		}
		c.Number = number.String
		c.Title = title.String
		c.Language = language.String
		if externalURL.Valid && externalURL.String != "" {
			c.ExternalURL = externalURL.String
		} else {
			c.ExternalURL = "https://mangadex.org/chapter/" + sourceID
		}
		if publishedAt.Valid {
			c.PublishedAt = time.Unix(publishedAt.Int64, 0).UTC()
		}
		c.Read = readFlag == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func includeReadFilter(include bool) string {
	if include {
		return "" // sin filtro extra: trae todos
	}
	return "AND (cur.published_at IS NULL OR c.published_at > cur.published_at)"
}

// markChapterRead avanza el cursor del entry al chapter dado, validando que
// el chapter pertenezca al manga del entry. La condición compuesta protege
// contra que un user marque caps de otros mangas (devolvería 0 rows).
func (h *Handler) markChapterRead(ctx context.Context, userID, entryID, chapterID int64) error {
	res, err := h.db.ExecContext(ctx, `
		UPDATE library_entries
		SET last_read_chapter_id = ?, updated_at = ?
		WHERE id = ?
		  AND user_id = ?
		  AND manga_id = (SELECT manga_id FROM chapter WHERE id = ?)
	`, chapterID, time.Now().UTC().Unix(), entryID, userID, chapterID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("entry/chapter mismatch")
	}
	return nil
}

// loadExternalURL devuelve la URL externa del cap (external_url si existe,
// fallback a mangadex.org/chapter/{source_id}).
func (h *Handler) loadExternalURL(ctx context.Context, chapterID int64) (string, error) {
	var (
		sourceID    string
		externalURL sql.NullString
	)
	err := h.db.QueryRowContext(ctx,
		`SELECT source_id, external_url FROM chapter WHERE id = ?`, chapterID,
	).Scan(&sourceID, &externalURL)
	if err != nil {
		return "", err
	}
	if externalURL.Valid && externalURL.String != "" {
		return externalURL.String, nil
	}
	return "https://mangadex.org/chapter/" + sourceID, nil
}
