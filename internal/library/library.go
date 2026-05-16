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
	"time"

	"git.odin.do/kengru/mokuji/internal/csrf"
	"git.odin.do/kengru/mokuji/internal/mangasource"
	"git.odin.do/kengru/mokuji/internal/releases/matcher"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/unsubscribe"
	"git.odin.do/kengru/mokuji/internal/version"
)

// Matcher resuelve el vínculo manga-de-AniList ↔ manga-de-MangaDex.
// Definido acá como interface para que el handler sea testeable sin red
// (en producción se cablea *matcher.Matcher en main.go).
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
	ID             int64
	Manga          Manga
	Status         string
	LastChapter    string
	IsFavorite     bool
	SourceURL      string
	Notes          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	MangadexStatus string // 'unmatched' | 'matched' | 'needs_review' | 'no_results'
	UnreadCount    int    // caps publicados después del cursor en preferred_language
	TotalChapters  int    // caps en DB para este manga en preferred_language
}

// Poller dispara un fetch sincrónico de capítulos para un manga recién
// matcheado. Implementado por internal/releases/worker.Worker. Opcional —
// si Config.Poller es nil, los caps llegan en el siguiente tick del worker
// periódico (hasta una hora de espera).
type Poller interface {
	PollOne(ctx context.Context, mangaID int64, mangadexID string) error
}

// Config agrupa las dependencias del handler.
type Config struct {
	DB            *sql.DB
	HTMLTemplates fs.FS
	MangaSource   mangasource.Source
	// Matcher es opcional. Si está cableado, el handler add llama Match
	// sincrónicamente después de persistir el manga y guarda el resultado
	// en manga.mangadex_id / manga.mangadex_status. Si es nil, los mangas
	// quedan con mangadex_status='unmatched' (default de la migration) y
	// el worker eventual los retomará en background.
	Matcher Matcher
	// Poller es opcional. Si está cableado, después de un match exitoso se
	// dispara un FetchFeed inmediato para tener capítulos visibles sin
	// esperar al tick del worker periódico. Ver Poller arriba.
	Poller Poller
	// Signer es opcional. Si está cableado, el handler monta GET /r/{token}
	// — el shortcut del email digest que marca leído y abre el lector en
	// una sola navegación. Sin signer, el endpoint queda no-existente y los
	// emails no tendrán links auto-mark.
	Signer *unsubscribe.Signer
	Logger *slog.Logger
}

// Handler sirve los endpoints de /library/*.
type Handler struct {
	db      *sql.DB
	src     mangasource.Source
	matcher Matcher
	poller  Poller
	signer  *unsubscribe.Signer
	pages   map[string]*template.Template
	logger  *slog.Logger
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

	funcs := template.FuncMap{
		"csrfInput":   csrf.Input,
		"statusLabel": statusLabel,
		"appVersion":  func() string { return version.Value },
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
		// library (ej. mangadex-badge). Lo agregamos primero para que cada
		// página lo tenga disponible. Nombre sin "_" porque go:embed
		// excluye archivos que empiezan con "_" o ".".
		if _, err := clone.ParseFS(cfg.HTMLTemplates, "library/partials.html", page); err != nil {
			return nil, fmt.Errorf("parsear %s: %w", page, err)
		}
		pages[page] = clone
	}

	return &Handler{
		db:      cfg.DB,
		src:     cfg.MangaSource,
		matcher: cfg.Matcher,
		poller:  cfg.Poller,
		signer:  cfg.Signer,
		pages:   pages,
		logger:  cfg.Logger,
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
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.render(w, "library/list.html", listData{
		Session: s,
		Entries: entries,
		Filter:  filter,
		Query:   query,
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
			data.Error = "no pudimos buscar en este momento. probá de nuevo."
		}
		data.Results = results
	}

	h.render(w, "library/search.html", data)
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
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.createEntry(r.Context(), s.UserID, mangaID); err != nil && !isUniqueConstraintError(err) {
		h.logger.Error("crear entry", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Intento de matching contra MangaDex. Cualquier fallo se loguea pero
	// no rompe el "agregar manga" — el manga queda como unmatched y un
	// worker periódico puede retomarlo. (Si Matcher no está cableado,
	// también queda unmatched y todo el sistema sigue funcionando.)
	h.tryMatchManga(r.Context(), mangaID, title)

	http.Redirect(w, r, "/library", http.StatusSeeOther)
}

// tryMatchManga corre el matcher sincrónicamente y persiste el resultado.
// Nunca devuelve error — el matching es best-effort y no debe bloquear el
// flujo de "agregar manga" si MangaDex está caído o rate limited.
func (h *Handler) tryMatchManga(ctx context.Context, mangaID int64, title string) {
	if h.matcher == nil {
		return
	}

	result, err := h.matcher.Match(ctx, title)
	if err != nil {
		// Rate limit y errores transitorios: dejamos el manga como
		// unmatched para que el worker lo retome más tarde.
		h.logger.Warn("matchear mangadex (queda unmatched)", "manga_id", mangaID, "title", title, "err", err)
		return
	}

	var mangadexID *string
	if result.Status == matcher.StatusMatched {
		id := result.MangaDexID
		mangadexID = &id
	}

	if err := h.updateMangadexMatch(ctx, mangaID, string(result.Status), mangadexID); err != nil {
		h.logger.Error("persistir match mangadex", "manga_id", mangaID, "err", err)
		return
	}

	// Si matchó y tenemos un poller cableado, disparamos un fetch inmediato.
	// El worker periódico tickea cada 1h — sin esto, un manga recién agregado
	// quedaría sin capítulos durante una ventana de hasta 60 minutos, lo cual
	// es UX inaceptable. Errores se loguean como warn; el worker eventual
	// retomará el manga porque next_check_at queda NULL hasta que el poll
	// termine.
	if h.poller != nil && mangadexID != nil {
		if err := h.poller.PollOne(ctx, mangaID, *mangadexID); err != nil {
			h.logger.Warn("poll inmediato tras match (worker retomará luego)",
				"manga_id", mangaID, "mangadex_id", *mangadexID, "err", err)
		}
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
		http.Error(w, "internal error", http.StatusInternalServerError)
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
		http.Error(w, "internal error", http.StatusInternalServerError)
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

	// Dos subqueries: total_chapters (en idioma del user) y unread_count
	// (caps publicados después del cursor). Distinguirlos permite que el
	// badge muestre "pendiente" (matched + 0 caps) vs "al día" (matched +
	// caps > 0 + UnreadCount = 0). El cursor NULL = leyó cero, cuenta todos.
	q := fmt.Sprintf(`
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
		WHERE %s
		ORDER BY le.is_favorite DESC, le.updated_at DESC
	`, where)

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
			&e.MangadexStatus, &e.TotalChapters, &e.UnreadCount,
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

// updateMangadexMatch persiste el resultado del matcher en la fila manga.
// mangadexID es *string para distinguir "match resuelto en NULL" (caso
// no_results / needs_review) de "match resuelto en un UUID concreto".
func (h *Handler) updateMangadexMatch(ctx context.Context, mangaID int64, status string, mangadexID *string) error {
	now := time.Now().UTC().Unix()
	var id any
	if mangadexID != nil {
		id = *mangadexID
	}
	_, err := h.db.ExecContext(ctx, `
		UPDATE manga
		SET mangadex_id = ?, mangadex_status = ?, mangadex_checked_at = ?
		WHERE id = ?
	`, id, status, now, mangaID)
	return err
}

// --- helpers ---

func (h *Handler) render(w http.ResponseWriter, page string, data any) {
	tmpl, ok := h.pages[page]
	if !ok {
		h.logger.Error("plantilla no encontrada", "page", page)
		http.Error(w, "internal error", http.StatusInternalServerError)
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
