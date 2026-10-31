// Package handler sirve los endpoints relacionados con releases:
//   - GET /releases -> lista flat de capítulos no leídos del usuario,
//     ordenados por publishAt descendente (más nuevos arriba).
//
// "No leído" se define vía el cursor numérico
// library_entries.last_read_chapter_number (migration 0008): un cap
// cuenta como no leído si CAST(c.number AS REAL) > cursor, o si el
// cursor es NULL (nunca marcó). Cursor por número permite dedup
// cross-source: ch70 publicado por dos scanlators cuenta como un solo
// cap leído cuando avanza el cursor.
package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/odin-software/mokuji/internal/csrf"
	"github.com/odin-software/mokuji/internal/errrender"
	"github.com/odin-software/mokuji/internal/feedback"
	"github.com/odin-software/mokuji/internal/session"
	"github.com/odin-software/mokuji/internal/version"
)

// MaxReleases es el cap de la página. Sin paginación por ahora — si un
// usuario tiene >100 caps acumulados es indicador de "ponete al día" más
// que de UX nuestra.
const MaxReleases = 100

// ReleasesPageSize es el tamaño de página de /releases. Cursor-based:
// la siguiente página continúa después del último (published_at, id) de
// la anterior, evitando que caps nuevos publicados durante la navegación
// salten o dupliquen items.
const ReleasesPageSize = 50

// Cursor es la posición de paginación. Zero-value = sin cursor (primera
// página). Before/BeforeID identifican el último item de la página
// anterior; la siguiente query devuelve filas estrictamente posteriores
// a ese punto en el orden (published_at DESC, id DESC).
type Cursor struct {
	Before   time.Time
	BeforeID int64
}

// IsZero reporta si el cursor es zero (sin paginación previa).
func (c Cursor) IsZero() bool {
	return c.Before.IsZero() && c.BeforeID == 0
}

// Config agrupa las dependencias del handler.
type Config struct {
	DB            *sql.DB
	HTMLTemplates fs.FS
	Logger        *slog.Logger
	Errs          *errrender.ErrorRenderer // nil en tests sin assets
}

// Handler sirve /releases.
type Handler struct {
	db     *sql.DB
	tmpl   *template.Template
	logger *slog.Logger
	errs   *errrender.ErrorRenderer
}

// Release es una fila de la página: un capítulo + metadata mínima del manga.
type Release struct {
	ChapterID     int64
	ChapterNumber string
	ChapterTitle  string
	Language      string
	PublishedAt   time.Time
	ReaderURL     string // ExternalURL si existe, sino mangadex.org/chapter/{source_id}
	MangaID       int64
	MangaTitle    string
	CoverURL      string
}

// New construye un Handler. Falla si templates no parsean.
func New(cfg Config) (*Handler, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	funcs := template.FuncMap{
		"csrfInput":       csrf.Input,
		"appVersion":      func() string { return version.Value },
		"feedbackEnabled": feedback.IsEnabled,
	}
	base, err := template.New("base").Funcs(funcs).ParseFS(cfg.HTMLTemplates, "layout.html")
	if err != nil {
		return nil, fmt.Errorf("parsear layout: %w", err)
	}
	page, err := base.Clone()
	if err != nil {
		return nil, fmt.Errorf("clonar base: %w", err)
	}
	if _, err := page.ParseFS(cfg.HTMLTemplates, "releases.html"); err != nil {
		return nil, fmt.Errorf("parsear releases.html: %w", err)
	}

	return &Handler{db: cfg.DB, tmpl: page, logger: cfg.Logger, errs: cfg.Errs}, nil
}

// Mount registra los endpoints en el mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /releases", h.list)
	mux.HandleFunc("POST /releases/mark-read", h.markRead)
	mux.HandleFunc("POST /releases/read", h.readAndOpen)
}

type pageData struct {
	Session    *session.Session
	Releases   []Release
	NextCursor *Cursor // nil = no hay más
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}

	cursor := parseReleasesCursor(r)
	releases, next, err := h.UnreadReleasesPage(r.Context(), s.UserID, ReleasesPageSize, cursor)
	if err != nil {
		h.logger.Error("listar releases", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "layout", pageData{
		Session:    s,
		Releases:   releases,
		NextCursor: next,
	}); err != nil {
		h.logger.Error("renderizar releases", "err", err)
	}
}

// parseReleasesCursor extrae el cursor de los query params. Tolerante:
// si los valores son inválidos o faltan, devuelve cursor zero (= primera
// página). Eso evita errores 400 por links viejos / bots con params
// raros.
func parseReleasesCursor(r *http.Request) Cursor {
	beforeRaw := r.URL.Query().Get("before")
	beforeIDRaw := r.URL.Query().Get("before_id")
	if beforeRaw == "" || beforeIDRaw == "" {
		return Cursor{}
	}
	beforeUnix, err := strconv.ParseInt(beforeRaw, 10, 64)
	if err != nil {
		return Cursor{}
	}
	beforeID, err := strconv.ParseInt(beforeIDRaw, 10, 64)
	if err != nil {
		return Cursor{}
	}
	return Cursor{
		Before:   time.Unix(beforeUnix, 0).UTC(),
		BeforeID: beforeID,
	}
}

// markRead avanza el cursor library_entries.last_read_chapter_number al
// MAX(cursor_actual, número del cap clickeado). Monotónico: leer cap 3
// después de cap 10 no retrocede el cursor. La subquery bloquea
// cualquier intento de marcar un cap que no pertenezca a un manga en
// la biblioteca del user (subselect devuelve NULL => 0 rows affected).
func (h *Handler) markRead(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	chapterID, err := strconv.ParseInt(r.PostFormValue("chapter_id"), 10, 64)
	if err != nil || chapterID <= 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	res, err := h.db.ExecContext(r.Context(), `
		UPDATE library_entries
		SET last_read_chapter_number = MAX(
		      COALESCE(last_read_chapter_number, 0),
		      (SELECT CAST(number AS REAL) FROM chapter WHERE id = ?)
		    ),
		    status = CASE WHEN status = 'planned' THEN 'reading' ELSE status END,
		    updated_at = ?
		WHERE user_id = ?
		  AND manga_id = (SELECT manga_id FROM chapter WHERE id = ?)
	`, chapterID, time.Now().UTC().Unix(), s.UserID, chapterID)
	if err != nil {
		h.logger.Error("marcar leído", "user_id", s.UserID, "chapter_id", chapterID, "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		http.NotFound(w, r)
		return
	}

	http.Redirect(w, r, "/releases", http.StatusSeeOther)
}

// readAndOpen sirve POST /releases/read. Equivalente al
// /library/entry/{id}/read pero accesible desde el feed /releases sin
// requerir el entry_id en el form (resolvemos manga_id desde chapter_id).
//
// Marca leído + responde con una página mínima que redirige al
// external_url. No usamos http.Redirect cross-origin: el CSP del server
// tiene form-action 'self' y aplicaría a redirects del form chain.
// Resolvemos vía <meta http-equiv="refresh"> que no cuenta como
// form-action.
func (h *Handler) readAndOpen(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	chapterID, err := strconv.ParseInt(r.PostFormValue("chapter_id"), 10, 64)
	if err != nil || chapterID <= 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// 1) Mark read — misma query que markRead, pero acá no redirigimos a
	// /releases, sino al external_url.
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE library_entries
		SET last_read_chapter_number = MAX(
		      COALESCE(last_read_chapter_number, 0),
		      (SELECT CAST(number AS REAL) FROM chapter WHERE id = ?)
		    ),
		    status = CASE WHEN status = 'planned' THEN 'reading' ELSE status END,
		    updated_at = ?
		WHERE user_id = ?
		  AND manga_id = (SELECT manga_id FROM chapter WHERE id = ?)
	`, chapterID, time.Now().UTC().Unix(), s.UserID, chapterID)
	if err != nil {
		h.logger.Error("read+open: marcar leído", "user_id", s.UserID, "chapter_id", chapterID, "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		http.NotFound(w, r)
		return
	}

	// 2) Load external URL. Fallback a mangadex si está vacío
	// (mismo patrón que UnreadReleases.ReaderURL).
	var external, sourceID, mangaID sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT external_url, source_id, manga_id FROM chapter WHERE id = ?`, chapterID,
	).Scan(&external, &sourceID, &mangaID); err != nil {
		h.logger.Error("read+open: cargar url", "chapter_id", chapterID, "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	url := external.String
	if url == "" && sourceID.Valid && sourceID.String != "" {
		url = "https://mangadex.org/chapter/" + sourceID.String
	}
	if url == "" {
		http.NotFound(w, r)
		return
	}

	writeRedirectPage(w, url)
}

// writeRedirectPage emite una página HTML mínima que redirige al destino
// via <meta http-equiv="refresh"> + fallback con <a> visible. Evita la
// restricción CSP form-action que se aplica al redirect chain de un
// http.Redirect.
var redirectTmpl = template.Must(template.New("redir").Parse(`<!DOCTYPE html>
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

// UnreadReleases devuelve los caps no leídos del usuario, ordenados por
// publishAt descendente. Filtros:
//   - solo entries del user con status='reading'
//   - solo caps en el idioma preferido del usuario
//   - solo caps publicados después del cursor (o todos si cursor NULL)
//
// Exportado para que el widget en home reuse el mismo query con un
// limit más chico, sin duplicar la query. Para paginación cursor-based
// usa UnreadReleasesPage.
func (h *Handler) UnreadReleases(ctx context.Context, userID int64, limit int) ([]Release, error) {
	// Dedup por (manga, number, language): elegimos el row con MAX
	// published_at por grupo (tie-break por id desc). Sin esto, un cap
	// publicado por dos scanlators en Comick aparecería dos veces en
	// /releases. El cursor compara CAST(c.number AS REAL) > cursor —
	// si el user marcó cap N, ningún row de ese número aparece (la
	// semántica monotónica por número resuelve el problema clásico de
	// "ya lo leí pero otro scanlator lo subió 2 días después").
	rows, err := h.db.QueryContext(ctx, `
		SELECT chapter.id, chapter.source_id, chapter.number, chapter.title, chapter.language,
		       chapter.published_at, chapter.external_url,
		       manga.id, manga.title, manga.cover_url
		FROM chapter
		JOIN manga          ON manga.id = chapter.manga_id
		JOIN library_entries le ON le.manga_id = manga.id
		JOIN users          ON users.id = le.user_id
		WHERE le.user_id = ?
		  AND le.status = 'reading'
		  AND chapter.language = users.preferred_language
		  AND (le.last_read_chapter_number IS NULL
		       OR CAST(chapter.number AS REAL) > le.last_read_chapter_number)
		  AND chapter.id = (
		    SELECT c2.id FROM chapter c2
		    WHERE c2.manga_id = chapter.manga_id
		      AND c2.number = chapter.number
		      AND c2.language = chapter.language
		    ORDER BY c2.published_at DESC, c2.id DESC
		    LIMIT 1
		  )
		ORDER BY chapter.published_at DESC, chapter.id DESC
		LIMIT ?
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReleases(rows)
}

// UnreadReleasesPage devuelve una página de caps no leídos con cursor.
// Si cursor.IsZero(), arranca desde el más reciente; si no, sigue después
// del item (published_at, id) indicado.
//
// Devuelve el slice de releases y un *Cursor para la siguiente página
// (nil si no hay más). Internamente fetcha limit+1 filas para detectar
// si hay más sin un COUNT extra: si vinieron más de limit, descarta la
// última fila y emite cursor; si vinieron <= limit, no hay más.
func (h *Handler) UnreadReleasesPage(ctx context.Context, userID int64, limit int, cursor Cursor) ([]Release, *Cursor, error) {
	args := []any{userID}
	cursorClause := ""
	if !cursor.IsZero() {
		cursorClause = `
			AND (chapter.published_at < ?
			     OR (chapter.published_at = ? AND chapter.id < ?))`
		unix := cursor.Before.UTC().Unix()
		args = append(args, unix, unix, cursor.BeforeID)
	}
	args = append(args, limit+1)

	query := `
		SELECT chapter.id, chapter.source_id, chapter.number, chapter.title, chapter.language,
		       chapter.published_at, chapter.external_url,
		       manga.id, manga.title, manga.cover_url
		FROM chapter
		JOIN manga          ON manga.id = chapter.manga_id
		JOIN library_entries le ON le.manga_id = manga.id
		JOIN users          ON users.id = le.user_id
		WHERE le.user_id = ?
		  AND le.status = 'reading'
		  AND chapter.language = users.preferred_language
		  AND (le.last_read_chapter_number IS NULL
		       OR CAST(chapter.number AS REAL) > le.last_read_chapter_number)
		  AND chapter.id = (
		    SELECT c2.id FROM chapter c2
		    WHERE c2.manga_id = chapter.manga_id
		      AND c2.number = chapter.number
		      AND c2.language = chapter.language
		    ORDER BY c2.published_at DESC, c2.id DESC
		    LIMIT 1
		  )` + cursorClause + `
		ORDER BY chapter.published_at DESC, chapter.id DESC
		LIMIT ?
	`

	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	out, err := scanReleases(rows)
	if err != nil {
		return nil, nil, err
	}

	if len(out) <= limit {
		return out, nil, nil
	}
	out = out[:limit]
	last := out[len(out)-1]
	return out, &Cursor{Before: last.PublishedAt, BeforeID: last.ChapterID}, nil
}

// scanReleases extrae las filas comunes de UnreadReleases y
// UnreadReleasesPage. Mantiene el bloque de Scan + conversión a Release
// en un solo lugar para que las dos queries no driften.
func scanReleases(rows *sql.Rows) ([]Release, error) {
	var out []Release
	for rows.Next() {
		var (
			r           Release
			sourceID    string
			number      sql.NullString
			title       sql.NullString
			language    sql.NullString
			publishedAt sql.NullInt64
			externalURL sql.NullString
			coverURL    sql.NullString
		)
		if err := rows.Scan(
			&r.ChapterID, &sourceID, &number, &title, &language,
			&publishedAt, &externalURL,
			&r.MangaID, &r.MangaTitle, &coverURL,
		); err != nil {
			return nil, err
		}
		r.ChapterNumber = number.String
		r.ChapterTitle = title.String
		r.Language = language.String
		if publishedAt.Valid {
			r.PublishedAt = time.Unix(publishedAt.Int64, 0).UTC()
		}
		r.CoverURL = coverURL.String
		if externalURL.Valid && externalURL.String != "" {
			r.ReaderURL = externalURL.String
		} else {
			r.ReaderURL = "https://mangadex.org/chapter/" + sourceID
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
