package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	htmltmpl "html/template"
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
	Sources       []EntrySource
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

	sources, err := h.loadEntrySources(r.Context(), entry.Manga.ID)
	if err != nil {
		h.logger.Error("entry detail: cargar sources", "err", err, "entry_id", entryID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.render(w, "library/entry.html", entryData{
		Session:     s,
		Entry:       entry,
		Chapters:    chapters,
		Sources:     sources,
		Sort:        sort,
		IncludeRead: includeRead,
		UnreadCount: entry.UnreadCount,
		// TotalChapters es el conteo real del manga en el idioma preferido
		// del user, no el del slice mostrado: ese cambia con include_read y
		// confunde ("0 capítulos" para un manga con 480 todos leídos).
		TotalChapters: entry.TotalChapters,
	})
}

// entryReadAndOpen sirve POST /library/entry/{id}/read.
//
// Marca leído + redirige al external_url del capítulo. No podemos usar
// http.Redirect cross-origin: el CSP del server tiene `form-action 'self'`
// y la spec dice que la directiva se aplica a TODOS los URLs del redirect
// chain de un form submission — el browser bloquearía el 303 a mangadex
// silenciosamente, dejando una pestaña en blanco. Por eso respondemos con
// HTML mínimo que dispara la navegación vía <meta http-equiv="refresh">;
// eso no cuenta como parte del form-action chain.
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

	writeRedirectPage(w, external)
}

// writeRedirectPage emite una página HTML mínima que redirige al destino
// via <meta http-equiv="refresh"> + fallback con <a> visible. Evita la
// restricción CSP form-action que se aplica al redirect chain de un
// http.Redirect. El destino se renderiza con html/template para escapar
// cualquier carácter peligroso. URL validada por context attribute.
var redirectTmpl = htmltmpl.Must(htmltmpl.New("redir").Parse(`<!DOCTYPE html>
<html lang="es"><head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="0; url={{.URL}}">
<title>abriendo el lector…</title>
</head><body>
<p>abriendo el lector… si no pasa nada, <a href="{{.URL}}" rel="noopener">click acá</a>.</p>
</body></html>`))

func writeRedirectPage(w http.ResponseWriter, url string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = redirectTmpl.Execute(w, struct{ URL string }{URL: url})
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
	q := fmt.Sprintf(`
		SELECT le.id, le.status, le.last_chapter, le.is_favorite, le.source_url, le.notes,
		       le.created_at, le.updated_at,
		       m.id, m.source, m.source_id, m.title, m.cover_url,
		       %s AS source_status,
		       (SELECT COUNT(DISTINCT c.number) FROM chapter c
		        WHERE c.manga_id = m.id AND c.language = u.preferred_language
		       ) AS total_chapters,
		       (SELECT COUNT(DISTINCT c.number) FROM chapter c
		        WHERE c.manga_id = m.id
		          AND c.language = u.preferred_language
		          AND (le.last_read_chapter_number IS NULL
		               OR CAST(c.number AS REAL) > le.last_read_chapter_number)
		       ) AS unread_count
		FROM library_entries le
		JOIN manga m ON m.id = le.manga_id
		JOIN users u ON u.id = le.user_id
		WHERE le.id = ? AND le.user_id = ?
	`, sourceStatusSummarySQL)
	err := h.db.QueryRowContext(ctx, q, entryID, userID).Scan(
		&e.ID, &e.Status, &lastCh, &isFav, &sourceURL, &notes,
		&created, &updated,
		&e.Manga.ID, &e.Manga.Source, &e.Manga.SourceID, &e.Manga.Title, &coverURL,
		&e.SourceStatus, &e.TotalChapters, &e.UnreadCount,
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

// loadChaptersForEntry trae los caps del manga en el idioma preferido
// del user, con dedup por número: si varias sources tienen el mismo cap
// (típico en Comick con N scanlators), elegimos solo el row con
// published_at máximo. El flag Read se calcula contra
// last_read_chapter_number — semántica monotónica por número.
func (h *Handler) loadChaptersForEntry(ctx context.Context, userID, mangaID int64, sort string, includeRead bool) ([]Chapter, error) {
	order := "DESC"
	if sort == "asc" {
		order = "ASC"
	}

	// El subquery "c.id = (...)" hace el dedup: por cada
	// (manga, number, language) elegimos la fila con MAX(published_at).
	// MAX(id) en caso de empate de published_at — determinístico.
	//
	// El read_flag compara el número del cap contra el cursor numérico.
	// Cursor NULL = nunca leyó = todos unread.
	q := fmt.Sprintf(`
		SELECT c.id, c.number, c.title, c.language, c.source_id, c.external_url, c.published_at,
		       CASE
		         WHEN le.last_read_chapter_number IS NULL THEN 0
		         WHEN CAST(c.number AS REAL) <= le.last_read_chapter_number THEN 1
		         ELSE 0
		       END AS read_flag
		FROM chapter c
		JOIN manga m ON m.id = c.manga_id
		JOIN users u ON u.id = ?
		JOIN library_entries le ON le.user_id = u.id AND le.manga_id = m.id
		WHERE m.id = ?
		  AND c.language = u.preferred_language
		  AND c.id = (
		    SELECT c2.id FROM chapter c2
		    WHERE c2.manga_id = c.manga_id
		      AND c2.number = c.number
		      AND c2.language = c.language
		    ORDER BY c2.published_at DESC, c2.id DESC
		    LIMIT 1
		  )
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
	return "AND (le.last_read_chapter_number IS NULL OR CAST(c.number AS REAL) > le.last_read_chapter_number)"
}

// markChapterRead avanza el cursor del entry al MAX(cursor_actual, número
// del cap marcado). Cursor monotónico: leer cap 3 después de cap 10 no
// retrocede. Valida que el chapter pertenezca al manga del entry (devuelve
// 0 rows si no — un user no puede mover el cursor de otro user).
func (h *Handler) markChapterRead(ctx context.Context, userID, entryID, chapterID int64) error {
	res, err := h.db.ExecContext(ctx, `
		UPDATE library_entries
		SET last_read_chapter_number = MAX(
		      COALESCE(last_read_chapter_number, 0),
		      (SELECT CAST(number AS REAL) FROM chapter WHERE id = ?)
		    ),
		    updated_at = ?
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
