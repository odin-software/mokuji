// Package library implementa los handlers de la biblioteca personal de cada
// usuario: search externo, add, list (con filtros), update, delete.
//
// La biblioteca está normalizada: el manga vive una vez en la DB (compartido
// entre usuarios) y cada usuario tiene un library_entry que apunta a él con
// sus campos personales (status, progreso, notas, favorito, source URL).
package library

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
	"strings"
	"sync"
	"time"

	"git.odin.do/kengru/mokuji/internal/csrf"
	"git.odin.do/kengru/mokuji/internal/errrender"
	"git.odin.do/kengru/mokuji/internal/feedback"
	"git.odin.do/kengru/mokuji/internal/mangasource"
	"git.odin.do/kengru/mokuji/internal/releases/matcher"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/unsubscribe"
	"git.odin.do/kengru/mokuji/internal/version"
)

// Matcher resuelve el vínculo entre un manga de la biblioteca y su
// contraparte en una source concreta. Una instancia = una source. Definido
// como interface para que el handler sea testeable sin red (en producción
// se cablea *matcher.Matcher en main.go por cada source habilitada).
type Matcher interface {
	Match(ctx context.Context, titles ...string) (matcher.Result, error)
}

const (
	maxLastChapter = 100
	maxNotes       = 5000
	maxSourceURL   = 2000
	maxQuery       = 200
)

var validStatuses = map[string]bool{
	"reading":   true,
	"planned":   true,
	"completed": true,
}

func statusLabel(s string) string {
	switch s {
	case "reading":
		return "leyendo"
	case "planned":
		return "por leer"
	case "completed":
		return "completado"
	}
	return s
}

// Manga es la metadata canónica del manga (compartida entre usuarios).
type Manga struct {
	ID       int64
	Source   string
	SourceID string
	Title    string
	CoverURL string
}

// Entry es un library_entry de un usuario sobre un manga.
type Entry struct {
	ID          int64
	Manga       Manga
	Status      string
	LastChapter string
	// LastReadChapterNumber es el cursor numérico de lectura (a diferencia de
	// LastChapter, que es texto libre que el user edita manualmente). Se usa
	// para derivar qué caps aparecen marcados como leídos y para el prefill
	// del form de "voy por el capítulo X" en entry.html.
	LastReadChapterNumber float64
	IsFavorite            bool
	SourceURL             string
	Notes                 string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	SourceStatus          matcher.Status // summary cross-source: matched si al menos una source matchea
	UnreadCount           int            // caps publicados después del cursor en preferred_language
	TotalChapters         int            // caps en DB para este manga en preferred_language
}

// Poller dispara un fetch sincrónico de capítulos para un (manga, source)
// recién matcheado. Implementado por internal/releases/worker.Worker.
// Opcional — si Config.Poller es nil, los caps llegan en el siguiente tick
// del worker periódico (hasta una hora de espera).
type Poller interface {
	PollOne(ctx context.Context, mangaID int64, source, sourceID string) error
}

// Config agrupa las dependencias del handler.
type Config struct {
	DB            *sql.DB
	HTMLTemplates fs.FS
	MangaSource   mangasource.Source
	// SourceMatchers mapea source name → matcher. El handler add corre
	// cada matcher después de persistir el manga y guarda el resultado
	// en una fila de manga_sources. Sources que no estén acá no tienen
	// fila y el worker no las polleea. Si el map está vacío, todos los
	// mangas quedan unmatched.
	SourceMatchers map[string]Matcher
	// Poller es opcional. Si está cableado, después de un match exitoso
	// se dispara un FetchChapters inmediato para tener capítulos visibles
	// sin esperar al tick del worker periódico.
	Poller Poller
	// Signer es opcional. Si está cableado, el handler monta GET /r/{token}
	// — el shortcut del email digest que marca leído y abre el lector en
	// una sola navegación.
	Signer *unsubscribe.Signer
	// BackgroundCtx es el contexto raíz para goroutines que viven más
	// allá de un request individual (típicamente PollOne disparado tras
	// matchear un manga). Se cancela al shutdown del server. Si es nil,
	// el handler usa context.Background() — los goroutines siguen vivos
	// hasta que terminan o el proceso muere.
	BackgroundCtx context.Context
	Logger        *slog.Logger
	Errs          *errrender.ErrorRenderer // nil en tests sin assets
}

// Handler sirve los endpoints de /library/*.
type Handler struct {
	db        *sql.DB
	src       mangasource.Source
	matchers  map[string]Matcher
	poller    Poller
	signer    *unsubscribe.Signer
	pages     map[string]*template.Template
	logger    *slog.Logger
	bgCtx     context.Context
	errs      *errrender.ErrorRenderer
}

// New construye un Handler. Falla si templates no parsean.
func New(cfg Config) (*Handler, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.MangaSource == nil {
		return nil, errors.New("MangaSource requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.BackgroundCtx == nil {
		cfg.BackgroundCtx = context.Background()
	}

	funcs := template.FuncMap{
		"csrfInput":       csrf.Input,
		"statusLabel":     statusLabel,
		"appVersion":      func() string { return version.Value },
		"feedbackEnabled": feedback.IsEnabled,
	}
	base, err := template.New("base").Funcs(funcs).ParseFS(cfg.HTMLTemplates, "layout.html")
	if err != nil {
		return nil, fmt.Errorf("parsear layout: %w", err)
	}

	pages := map[string]*template.Template{}
	for _, page := range []string{"library/list.html", "library/search.html", "library/entry.html"} {
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("clonar base para %s: %w", page, err)
		}
		// partials.html contiene defines compartidos por las páginas de
		// library (ej. source-badge). Lo agregamos primero para que cada
		// página lo tenga disponible. Nombre sin "_" porque go:embed
		// excluye archivos que empiezan con "_" o ".".
		if _, err := clone.ParseFS(cfg.HTMLTemplates, "library/partials.html", page); err != nil {
			return nil, fmt.Errorf("parsear %s: %w", page, err)
		}
		pages[page] = clone
	}

	return &Handler{
		db:       cfg.DB,
		src:      cfg.MangaSource,
		matchers: cfg.SourceMatchers,
		poller:   cfg.Poller,
		signer:   cfg.Signer,
		pages:    pages,
		logger:   cfg.Logger,
		bgCtx:    cfg.BackgroundCtx,
		errs:     cfg.Errs,
	}, nil
}

// Mount registra los endpoints en el mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /library", h.list)
	mux.HandleFunc("GET /library/search", h.search)
	mux.HandleFunc("POST /library/add", h.add)
	mux.HandleFunc("POST /library/{id}/update", h.update)
	mux.HandleFunc("POST /library/{id}/delete", h.delete)
	mux.HandleFunc("GET /library/entry/{id}", h.entryDetail)
	mux.HandleFunc("POST /library/entry/{id}/read", h.entryReadAndOpen)
	mux.HandleFunc("POST /library/entry/{id}/mark-read", h.entryMarkRead)
	mux.HandleFunc("POST /library/entry/{id}/cursor", h.entryCursor)
	mux.HandleFunc("POST /library/entry/{id}/sources/{source}/retry", h.entrySourceRetry)
	mux.HandleFunc("POST /library/entry/{id}/sources/{source}/unlink", h.entrySourceUnlink)
	if h.signer != nil {
		mux.HandleFunc("GET /r/{token}", h.emailRead)
	}
}

func (h *Handler) requireSession(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return nil, false
	}
	return s, true
}

type listData struct {
	Session *session.Session
	Entries []Entry
	Filter  string
	Query   string
	// UserSlug es el slug del user logueado. Vacío si no eligió. Sirve
	// para construir el link "tu vista pública" sin tirar al 301 cuando
	// ya tiene username (cosmético: /u/alice se ve mejor que /u/12).
	UserSlug string
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	if filter != "all" && !validStatuses[filter] {
		filter = "all"
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > maxQuery {
		query = query[:maxQuery]
	}

	entries, err := h.listEntries(r.Context(), s.UserID, filter, query)
	if err != nil {
		h.logger.Error("listar biblioteca", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	// Slug del user para el link "tu vista pública". Best-effort: si
	// falla, dejamos vacío y el template cae al id (con 301 post-load).
	var slug sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT slug FROM users WHERE id = ?`, s.UserID,
	).Scan(&slug); err != nil {
		h.logger.Warn("cargar slug para list", "user_id", s.UserID, "err", err)
	}

	h.render(w, r, "library/list.html", listData{
		Session:  s,
		Entries:  entries,
		Filter:   filter,
		Query:    query,
		UserSlug: slug.String,
	})
}

type searchData struct {
	Session *session.Session
	Query   string
	Source  string
	Results []mangasource.SearchResult
	Error   string
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > maxQuery {
		query = query[:maxQuery]
	}

	data := searchData{
		Session: s,
		Query:   query,
		Source:  h.src.Name(),
	}

	if query != "" {
		results, err := h.src.Search(r.Context(), query)
		if err != nil {
			h.logger.Error("buscar en fuente externa", "err", err, "query", query)
			data.Error = "no pudimos buscar en este momento. prueba de nuevo."
		}
		data.Results = results
	}

	h.render(w, r, "library/search.html", data)
}

func (h *Handler) add(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	source := r.PostFormValue("source")
	sourceID := r.PostFormValue("source_id")
	title := strings.TrimSpace(r.PostFormValue("title"))
	coverURL := strings.TrimSpace(r.PostFormValue("cover_url"))

	if source == "" || sourceID == "" || title == "" {
		http.Error(w, "datos insuficientes", http.StatusBadRequest)
		return
	}

	mangaID, err := h.upsertManga(r.Context(), source, sourceID, title, coverURL)
	if err != nil {
		h.logger.Error("upsert manga", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	if err := h.createEntry(r.Context(), s.UserID, mangaID); err != nil && !isUniqueConstraintError(err) {
		h.logger.Error("crear entry", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	// Intento de matching contra cada source configurada. Errores per-source
	// se loguean y no rompen el flow — la fila de manga_sources queda en el
	// estado correspondiente y el worker la retoma luego (o el user puede
	// "reintentar" desde el detail page más adelante).
	h.tryMatchSources(r.Context(), mangaID, title)

	http.Redirect(w, r, "/library", http.StatusSeeOther)
}

// tryMatchSources corre cada matcher configurado contra el manga en
// PARALELO y persiste una fila por (manga, source) en manga_sources.
// Bloquea hasta que todos los matchers respondan o ctx expire — eso
// asegura que cuando el handler retorne al user, el detail page muestre
// el estado correcto de cada source.
//
// Después de matchear, dispara PollOne en goroutines fire-and-forget
// con el background context del handler (NO el del request, que muere
// al hacer redirect). Sin esto, mangas grandes como One Piece (que
// tienen 1000+ caps a paginar con rate limit) colgaban la request
// durante minutos.
//
// Nunca propaga errores: el matching es best-effort y no debe bloquear
// "agregar manga". Sources que fallan quedan sin fila y el worker
// periódico las retoma; el user puede reintentar desde el detail page.
func (h *Handler) tryMatchSources(ctx context.Context, mangaID int64, title string) {
	if len(h.matchers) == 0 {
		return
	}
	var wg sync.WaitGroup
	for sourceName, m := range h.matchers {
		wg.Add(1)
		go func(name string, mc Matcher) {
			defer wg.Done()
			h.tryMatchOne(ctx, mangaID, name, mc, title)
		}(sourceName, m)
	}
	wg.Wait()
}

func (h *Handler) tryMatchOne(ctx context.Context, mangaID int64, sourceName string, m Matcher, title string) {
	result, err := m.Match(ctx, title)
	if err != nil {
		// Rate limit y errores transitorios: no insertamos fila para
		// que el user pueda reintentar manualmente o el worker periódico
		// retome cuando vuelva el servicio.
		h.logger.Warn("matchear source (sin fila persistida)",
			"manga_id", mangaID, "source", sourceName, "title", title, "err", err)
		return
	}

	var sourceID string
	if result.Status == matcher.StatusMatched {
		sourceID = result.SourceID
	}

	if err := h.upsertMangaSource(ctx, mangaID, sourceName, string(result.Status), sourceID); err != nil {
		h.logger.Error("persistir manga_source",
			"manga_id", mangaID, "source", sourceName, "err", err)
		return
	}

	// Poll fire-and-forget si matchó y hay poller cableado. Usa el
	// background context del handler — NO el del request — porque el
	// poll puede tardar minutos (One Piece: ~1182 caps paginados con
	// rate limit). El bgCtx se cancela en server shutdown, así que el
	// goroutine no leakea más allá del proceso.
	if h.poller != nil && result.Status == matcher.StatusMatched && sourceID != "" {
		go func() {
			if err := h.poller.PollOne(h.bgCtx, mangaID, sourceName, sourceID); err != nil {
				h.logger.Warn("poll async tras match (worker retomará luego)",
					"manga_id", mangaID, "source", sourceName, "source_id", sourceID, "err", err)
			}
		}()
	}
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	status := r.PostFormValue("status")
	if !validStatuses[status] {
		status = "reading"
	}

	lastCh := strings.TrimSpace(r.PostFormValue("last_chapter"))
	if len(lastCh) > maxLastChapter {
		lastCh = lastCh[:maxLastChapter]
	}

	sourceURL := strings.TrimSpace(r.PostFormValue("source_url"))
	if len(sourceURL) > maxSourceURL {
		sourceURL = sourceURL[:maxSourceURL]
	}

	notes := strings.TrimSpace(r.PostFormValue("notes"))
	if len(notes) > maxNotes {
		notes = notes[:maxNotes]
	}

	isFav := r.PostFormValue("is_favorite") == "on"

	res, err := h.db.ExecContext(r.Context(), `
		UPDATE library_entries
		SET status = ?, last_chapter = ?, source_url = ?, notes = ?, is_favorite = ?, updated_at = ?
		WHERE id = ? AND user_id = ?
	`, status, lastCh, sourceURL, notes, boolToInt(isFav), time.Now().UTC().Unix(), id, s.UserID)
	if err != nil {
		h.logger.Error("actualizar entry", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		http.NotFound(w, r)
		return
	}

	http.Redirect(w, r, "/library", http.StatusSeeOther)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM library_entries WHERE id = ? AND user_id = ?`,
		id, s.UserID,
	)
	if err != nil {
		h.logger.Error("borrar entry", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		http.NotFound(w, r)
		return
	}

	http.Redirect(w, r, "/library", http.StatusSeeOther)
}

// --- DB helpers ---

// sourceStatusSummarySQL es el snippet SQL que computa el status
// cross-source de un manga: matched si al menos una source matchea,
// si no needs_review, si no no_results, default unmatched. Está aislado
// como const para reusar entre listEntries y loadEntry. La subquery
// referencia m.id del SELECT externo.
const sourceStatusSummarySQL = `
COALESCE((
	SELECT CASE
	  WHEN SUM(status = 'matched') > 0 THEN 'matched'
	  WHEN SUM(status = 'needs_review') > 0 THEN 'needs_review'
	  WHEN SUM(status = 'no_results') > 0 THEN 'no_results'
	  ELSE 'unmatched'
	END
	FROM manga_sources WHERE manga_id = m.id
), 'unmatched')`

func (h *Handler) listEntries(ctx context.Context, userID int64, filter, query string) ([]Entry, error) {
	args := []any{userID}
	where := "le.user_id = ?"

	if filter != "all" {
		where += " AND le.status = ?"
		args = append(args, filter)
	}
	if query != "" {
		where += " AND m.title LIKE ?"
		args = append(args, "%"+query+"%")
	}

	// Dos subqueries de count: total_chapters (números únicos en idioma
	// del user) y unread_count (números únicos posteriores al cursor).
	// DISTINCT c.number: con multi-source un mismo cap puede tener N
	// filas (uno por scanlator), pero al user le importa el cap, no la
	// fuente. Cursor NULL = nunca leyó, cuenta todos.
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
		WHERE %s
		ORDER BY CASE le.status
		             WHEN 'reading'   THEN 0
		             WHEN 'planned'   THEN 1
		             WHEN 'completed' THEN 2
		             ELSE 3
		         END ASC,
		         le.is_favorite DESC,
		         le.updated_at DESC
	`, sourceStatusSummarySQL, where)

	rows, err := h.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		var isFav int
		var lastCh, sourceURL, notes, coverURL sql.NullString
		var created, updated int64
		if err := rows.Scan(
			&e.ID, &e.Status, &lastCh, &isFav, &sourceURL, &notes,
			&created, &updated,
			&e.Manga.ID, &e.Manga.Source, &e.Manga.SourceID, &e.Manga.Title, &coverURL,
			&e.SourceStatus, &e.TotalChapters, &e.UnreadCount,
		); err != nil {
			return nil, err
		}
		e.IsFavorite = isFav == 1
		e.LastChapter = lastCh.String
		e.SourceURL = sourceURL.String
		e.Notes = notes.String
		e.Manga.CoverURL = coverURL.String
		e.CreatedAt = time.Unix(created, 0).UTC()
		e.UpdatedAt = time.Unix(updated, 0).UTC()
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (h *Handler) upsertManga(ctx context.Context, source, sourceID, title, coverURL string) (int64, error) {
	now := time.Now().UTC().Unix()
	var id int64
	err := h.db.QueryRowContext(ctx, `
		INSERT INTO manga (source, source_id, title, cover_url, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, source_id) DO UPDATE
		SET title = excluded.title, cover_url = excluded.cover_url, updated_at = excluded.updated_at
		RETURNING id
	`, source, sourceID, title, coverURL, now, now).Scan(&id)
	return id, err
}

func (h *Handler) createEntry(ctx context.Context, userID, mangaID int64) error {
	now := time.Now().UTC().Unix()
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at)
		VALUES (?, ?, 'reading', ?, ?)
	`, userID, mangaID, now, now)
	return err
}

// upsertMangaSource persiste el resultado del matcher como una fila de
// manga_sources. sourceID puede ser vacío (caso needs_review / no_results):
// la columna sigue siendo NOT NULL así que guardamos '' — el worker filtra
// estas filas con `source_id != ''`. ON CONFLICT update cubre el caso de
// "reintentar matching" (el user dispara una segunda vez).
func (h *Handler) upsertMangaSource(ctx context.Context, mangaID int64, source, status, sourceID string) error {
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO manga_sources (manga_id, source, source_id, status, checked_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (manga_id, source) DO UPDATE
		SET source_id = excluded.source_id,
		    status    = excluded.status,
		    checked_at = excluded.checked_at
	`, mangaID, source, sourceID, status, time.Now().UTC().Unix())
	return err
}

// --- helpers ---

func (h *Handler) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	tmpl, ok := h.pages[page]
	if !ok {
		h.logger.Error("plantilla no encontrada", "page", page)
		h.errs.InternalServerError(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("renderizar plantilla", "page", page, "err", err)
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUniqueConstraintError detecta el error específico que retorna SQLite
// (vía modernc.org/sqlite) cuando se viola una UNIQUE constraint. Lo usamos
// para tratar "agregar manga que ya está" como caso no-fatal.
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
