package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	htmltmpl "html/template"
	"net/http"
	"strconv"
	"strings"
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

// EntryChaptersPageSize es el tamaño de página de la chapter list del
// entry detail. Offset-based: la lista es estable (no llegan caps al
// medio del rango histórico), así que el user puede saltar a "página N"
// predecible.
const EntryChaptersPageSize = 100

// Chapter es una fila de la lista de caps en la vista detail. Con
// multi-source, un cap (mismo número + idioma) puede estar disponible
// en N sources/scanlators distintos — esa lista vive en ReadOptions.
// Los campos "primarios" (ID, ExternalURL) corresponden a la primera
// opción, que es la más recientemente publicada en alguna source.
type Chapter struct {
	ID          int64
	Number      string
	Title       string
	Language    string
	ExternalURL string          // URL de la opción primaria (= ReadOptions[0].URL)
	PublishedAt time.Time       // published_at de la opción primaria
	Read        bool            // true si este cap está en o antes del cursor
	ReadOptions []ChapterOption // todas las opciones de lectura para este (manga, number, language)
}

// ChapterOption es una de las opciones disponibles para leer un cap —
// un row específico de la tabla chapter, ya con la URL del lector y
// metadata útil para mostrar al user (scanlator, source).
type ChapterOption struct {
	ID          int64  // chapter.id; permite "marcar leído" apuntando a un row concreto
	Source      string // 'mangadex' | 'asura' | 'comick'
	Label       string // nombre del scanlator (de chapter.title si lo trae el cliente)
	ExternalURL string
	PublishedAt time.Time
}

type entryData struct {
	Session  *session.Session
	Entry    Entry
	Chapters []Chapter
	Sources  []EntrySource
	// SourcesSummary es la línea bajo el título (ver sourcesSummary).
	SourcesSummary string
	// Next es el primer capítulo después del cursor (nil si estás al día) y
	// Latest el número del último disponible. Ver loadNextChapter.
	Next   *Chapter
	Latest string
	Sort           string // "asc" | "desc"
	IncludeRead    bool
	UnreadCount    int
	TotalChapters  int

	// Paginación de la chapter list.
	Page       int  // 1-indexed; siempre >= 1
	TotalPages int  // total de páginas (1 si no hay overflow)
	HasPrev    bool // Page > 1
	HasNext    bool // Page < TotalPages
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
		h.errs.InternalServerError(w, r)
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
	// Default: ocultar leídos para que la vista priorice lo nuevo. Si el
	// user no tiene unread, fallback a mostrar leídos — sino la página
	// queda vacía cuando está al día con un manga.
	// ?include_read=0 fuerza ocultar; ?include_read=1 fuerza mostrar.
	includeReadParam := r.URL.Query().Get("include_read")
	var includeRead bool
	switch includeReadParam {
	case "1":
		includeRead = true
	case "0":
		includeRead = false
	default:
		includeRead = entry.UnreadCount == 0
	}

	chapters, err := h.loadChaptersForEntry(r.Context(), s.UserID, entry.Manga.ID, sort, includeRead)
	if err != nil {
		h.logger.Error("entry detail: cargar chapters", "err", err, "entry_id", entryID)
		h.errs.InternalServerError(w, r)
		return
	}

	sources, err := h.loadEntrySources(r.Context(), entry.Manga.ID)
	if err != nil {
		h.logger.Error("entry detail: cargar sources", "err", err, "entry_id", entryID)
		h.errs.InternalServerError(w, r)
		return
	}

	next, latest, err := h.loadNextChapter(r.Context(), s.UserID, entry.Manga.ID)
	if err != nil {
		h.logger.Error("entry detail: cargar siguiente cap", "err", err, "entry_id", entryID)
		h.errs.InternalServerError(w, r)
		return
	}

	// Paginación 1-indexed. Default 1, invalid → 1, out-of-bounds → clamp a TotalPages.
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}

	totalGroups := len(chapters)
	totalPages := max((totalGroups+EntryChaptersPageSize-1)/EntryChaptersPageSize, 1)
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * EntryChaptersPageSize
	end := min(start+EntryChaptersPageSize, totalGroups)
	pageChapters := chapters[start:end]

	h.render(w, r, "library/entry.html", entryData{
		Session:        s,
		Entry:          entry,
		Chapters:       pageChapters,
		Sources:        sources,
		SourcesSummary: sourcesSummary(sources),
		Next:           next,
		Latest:         latest,
		Sort:           sort,
		IncludeRead:    includeRead,
		UnreadCount:    entry.UnreadCount,
		// TotalChapters es el conteo real del manga en el idioma preferido
		// del user, no el del slice mostrado: ese cambia con include_read y
		// confunde ("0 capítulos" para un manga con 480 todos leídos).
		TotalChapters: entry.TotalChapters,
		Page:          page,
		TotalPages:    totalPages,
		HasPrev:       page > 1,
		HasNext:       page < totalPages,
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
		h.errs.InternalServerError(w, r)
		return
	}

	external, err := h.loadExternalURL(r.Context(), chapterID)
	if err != nil {
		h.logger.Error("read+open: external url", "err", err, "chapter_id", chapterID)
		h.errs.InternalServerError(w, r)
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
<p>abriendo el lector… si no pasa nada, <a href="{{.URL}}" rel="noopener">toca aquí</a>.</p>
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
		h.errs.InternalServerError(w, r)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/library/entry/%d#capitulos", entry.ID), http.StatusSeeOther)
}

// entryCursor sirve POST /library/entry/{id}/cursor. Setea el cursor de
// lectura a un valor arbitrario — explícitamente permite retroceso
// (a diferencia de markChapterRead, que es monotónico). Útil para
// corregir errores de mark-read o para catalogar manga ya leído.
func (h *Handler) entryCursor(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	entryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || entryID <= 0 {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "form inválido", http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(r.PostFormValue("last_read_chapter_number"))
	cursor, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		http.Error(w, "valor inválido para cursor", http.StatusBadRequest)
		return
	}
	if cursor < 0 {
		http.Error(w, "cursor no puede ser negativo", http.StatusBadRequest)
		return
	}

	res, err := h.db.ExecContext(r.Context(), `
		UPDATE library_entries
		SET last_read_chapter_number = ?,
		    status = CASE WHEN ? > 0 THEN `+promotePlannedSQL+` ELSE status END,
		    updated_at = ?
		WHERE id = ? AND user_id = ?
	`, cursor, cursor, time.Now().UTC().Unix(), entryID, s.UserID)
	if err != nil {
		h.logger.Error("set cursor", "err", err, "entry_id", entryID, "user_id", s.UserID)
		h.errs.InternalServerError(w, r)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Entry no existe o pertenece a otro user. 404 sin revelar.
		http.NotFound(w, r)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/library/entry/%d#cursor", entryID), http.StatusSeeOther)
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
		h.errs.InternalServerError(w, r)
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
		e                Entry
		isFav            int
		lrcn             sql.NullFloat64
		sourceURL        sql.NullString
		notes            sql.NullString
		coverURL         sql.NullString
		created, updated int64
	)
	q := fmt.Sprintf(`
		SELECT le.id, le.status, le.last_read_chapter_number, le.is_favorite, le.source_url, le.notes,
		       le.created_at, le.updated_at,
		       m.id, m.source, m.source_id, m.title, m.cover_url,
		       %s AS source_status,
		       %s AS searching,
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
	`, sourceStatusSummarySQL, searchingSQL)
	err := h.db.QueryRowContext(ctx, q, entryID, userID).Scan(
		&e.ID, &e.Status, &lrcn, &isFav, &sourceURL, &notes,
		&created, &updated,
		&e.Manga.ID, &e.Manga.Source, &e.Manga.SourceID, &e.Manga.Title, &coverURL,
		&e.SourceStatus, &e.Searching, &e.TotalChapters, &e.UnreadCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	e.IsFavorite = isFav == 1
	e.LastReadChapterNumber = lrcn.Float64 // 0 si NULL — comportamiento esperado para entries nuevos
	e.SourceURL = sourceURL.String
	e.Notes = notes.String
	e.Manga.CoverURL = coverURL.String
	e.CreatedAt = time.Unix(created, 0).UTC()
	e.UpdatedAt = time.Unix(updated, 0).UTC()
	return e, true, nil
}

// loadChaptersForEntry trae los caps del manga en el idioma preferido
// del user. La consulta NO dedupa — devuelve una fila por (cap, source);
// el agrupamiento por número sucede en Go, donde armamos un slice de
// ChapterOption por cada grupo. El user ve un row por número con la
// lista de "leer en X" disponibles.
//
// ORDER BY por número (no published_at) porque agregadores como Comick
// suben archivos históricos completos cuando se agrega un scanlator
// nuevo: el published_at refleja "cuándo apareció en la plataforma",
// no la cronología. Ordenar por número da la progresión esperada para
// ponerse al día.
// loadNextChapter busca el CTA "sigue con el cap. N" (D6): el menor
// capítulo numérico después del cursor, en el idioma del user; si hay
// varias filas con ese número, la más reciente. latest es el mayor número
// numérico tal cual está guardado. Los no numéricos ("Special", "Extra")
// se ignoran: CAST los convierte en 0 y quedarían siempre primero.
func (h *Handler) loadNextChapter(ctx context.Context, userID, mangaID int64) (*Chapter, string, error) {
	const base = `
		FROM chapter c
		JOIN users u ON u.id = ?
		JOIN library_entries le ON le.user_id = u.id AND le.manga_id = c.manga_id
		WHERE c.manga_id = ?
		  AND c.language = u.preferred_language
		  AND c.number GLOB '[0-9]*'`

	var latest string
	err := h.db.QueryRowContext(ctx, `SELECT c.number `+base+`
		ORDER BY CAST(c.number AS REAL) DESC LIMIT 1`, userID, mangaID).Scan(&latest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}

	var next Chapter
	err = h.db.QueryRowContext(ctx, `SELECT c.id, c.number `+base+`
		  AND CAST(c.number AS REAL) > COALESCE(le.last_read_chapter_number, -1)
		ORDER BY CAST(c.number AS REAL) ASC, c.published_at DESC, c.id DESC
		LIMIT 1`, userID, mangaID).Scan(&next.ID, &next.Number)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, latest, nil
	}
	if err != nil {
		return nil, "", err
	}
	return &next, latest, nil
}

func (h *Handler) loadChaptersForEntry(ctx context.Context, userID, mangaID int64, sort string, includeRead bool) ([]Chapter, error) {
	order := "DESC"
	if sort == "asc" {
		order = "ASC"
	}

	q := fmt.Sprintf(`
		SELECT c.id, c.number, c.title, c.language, c.source, c.source_id, c.external_url, c.published_at, c.group_name,
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
		  %s
		ORDER BY CAST(c.number AS REAL) %s, c.published_at DESC, c.id DESC
	`, includeReadFilter(includeRead), order)

	rows, err := h.db.QueryContext(ctx, q, userID, mangaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Agrupamos por (number, language). El orden del SQL garantiza que
	// la primera fila de cada grupo sea la opción primaria (más reciente
	// por published_at). Las siguientes son alternativas.
	type groupKey struct{ num, lang string }
	groups := map[groupKey]int{} // → index en out
	var out []Chapter

	for rows.Next() {
		var (
			rowID       int64
			number      sql.NullString
			title       sql.NullString
			language    sql.NullString
			source      string
			sourceID    string
			externalURL sql.NullString
			publishedAt sql.NullInt64
			groupName   sql.NullString
			readFlag    int
		)
		if err := rows.Scan(&rowID, &number, &title, &language, &source, &sourceID, &externalURL, &publishedAt, &groupName, &readFlag); err != nil {
			return nil, err
		}
		var pub time.Time
		if publishedAt.Valid {
			pub = time.Unix(publishedAt.Int64, 0).UTC()
		}
		// Construir la URL del lector con fallback a mangadex.org si la
		// source no nos pasó external_url (caso histórico de mangadex
		// donde guardábamos solo el UUID).
		url := externalURL.String
		if url == "" {
			url = "https://mangadex.org/chapter/" + sourceID
		}
		// Label del botón: nombre del scanlator/grupo cuando la source
		// lo trae (Comick lo separa de title). Sources con un solo
		// grupo (Asura, MangaDex) lo dejan vacío — cae al source name
		// para que el botón diga "leer en asura" / "leer en mangadex".
		label := groupName.String
		if label == "" {
			label = source
		}

		key := groupKey{num: number.String, lang: language.String}
		idx, exists := groups[key]
		if !exists {
			// Primer row del grupo: éste define los campos primarios.
			out = append(out, Chapter{
				ID:          rowID,
				Number:      number.String,
				Title:       title.String,
				Language:    language.String,
				ExternalURL: url,
				PublishedAt: pub,
				Read:        readFlag == 1,
				ReadOptions: []ChapterOption{{
					ID:          rowID,
					Source:      source,
					Label:       label,
					ExternalURL: url,
					PublishedAt: pub,
				}},
			})
			groups[key] = len(out) - 1
			continue
		}
		// Row adicional del mismo grupo: solo agregamos a ReadOptions.
		// El Title del grupo se pobla con el primer row que lo traiga
		// no-vacío (típicamente uno con título de la historia tipo "Zaza").
		if out[idx].Title == "" && title.Valid {
			out[idx].Title = title.String
		}
		out[idx].ReadOptions = append(out[idx].ReadOptions, ChapterOption{
			ID:          rowID,
			Source:      source,
			Label:       label,
			ExternalURL: url,
			PublishedAt: pub,
		})
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
// promotePlannedSQL es la regla de D3: cualquier progreso pasa "por leer" a
// "leyendo" (solo esos se pollean y se avisan). Nunca toca "completado":
// avanzar ahí es una relectura, no un cambio de estado.
const promotePlannedSQL = `CASE WHEN status = 'planned' THEN 'reading' ELSE status END`

func (h *Handler) markChapterRead(ctx context.Context, userID, entryID, chapterID int64) error {
	res, err := h.db.ExecContext(ctx, `
		UPDATE library_entries
		SET last_read_chapter_number = MAX(
		      COALESCE(last_read_chapter_number, 0),
		      (SELECT CAST(number AS REAL) FROM chapter WHERE id = ?)
		    ),
		    status = `+promotePlannedSQL+`,
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
