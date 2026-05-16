// Package worker corre el job en background que polleea cada source de
// release tracking por capítulos nuevos. Diseño completo en ADR 0014.
//
// Una goroutine única dentro del proceso de mokuji, schedule con
// time.Ticker. En cada tick, query SQL devuelve hasta BatchSize filas de
// manga_sources cuyo next_check_at venció, y por cada una se hace 1
// request al cliente correspondiente (lookup vía Clients[source]).
// Capítulos nuevos se persisten en la tabla chapter (con source =
// nombre de la fuente); next_check_at se setea a now+RecheckInterval.
//
// Cola implícita en manga_sources (columna next_check_at) — sin tabla
// de jobs aparte.
package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"git.odin.do/kengru/mokuji/internal/releasesource"
)

// SourceHealth es un snapshot de la salud de una source en memoria.
// Lo emite Worker.Health() y se renderiza en /admin/sources-health.
//
// "Salud" acá significa últimos resultados del worker — un last_success_at
// reciente significa que la source responde. last_error_at + consecutive_failures
// indican degradación. Sin actividad reciente (ambos zero) NO es error —
// puede que las series matched estén al día y no hubo nada para chequear.
type SourceHealth struct {
	LastSuccessAt       time.Time `json:"last_success_at,omitzero"`
	LastErrorAt         time.Time `json:"last_error_at,omitzero"`
	LastError           string    `json:"last_error,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
}

// maxErrorMessage cota cuánto del mensaje de error guardamos en memoria
// para el endpoint admin. Mensajes con HTML o stack traces de 5xx pueden
// ser grandes; 200 chars es suficiente para diagnosticar sin desperdiciar.
const maxErrorMessage = 200

// Defaults conservadores. En producción se overridean vía Config.
const (
	DefaultTickInterval    = 1 * time.Hour
	DefaultRecheckInterval = 6 * time.Hour
	// RateLimitedDelay es cuánto esperamos antes de retomar una fila que
	// nos devolvió ErrRateLimited. Más que el tick para que no monopolice.
	RateLimitedDelay = 15 * time.Minute
	// BatchSize es cuántas filas procesamos por tick. Cota para no
	// bloquear demasiado tiempo en el loop si la cola creció.
	BatchSize = 50
)

// Config agrupa las dependencias.
type Config struct {
	DB *sql.DB
	// Clients mapea nombre de source → cliente. El nombre debe coincidir
	// con SourceClient.Name() y con manga_sources.source. Sources que no
	// están en el map (deshabilitadas en config) se saltan: sus filas en
	// manga_sources quedan dormant sin error.
	Clients         map[string]releasesource.Client
	Logger          *slog.Logger
	TickInterval    time.Duration // 0 -> DefaultTickInterval
	RecheckInterval time.Duration // 0 -> DefaultRecheckInterval
	Language        string        // "" -> "en"
}

// Worker es el job en background.
type Worker struct {
	db              *sql.DB
	clients         map[string]releasesource.Client
	logger          *slog.Logger
	tickInterval    time.Duration
	recheckInterval time.Duration
	language        string

	// health rastrea el resultado de la última operación contra cada
	// source. Lo lee el endpoint /admin/sources-health. Protegido por
	// healthMu porque pollSource puede correr concurrente al admin GET.
	healthMu sync.Mutex
	health   map[string]*SourceHealth
}

// New construye un Worker con defaults razonables.
func New(cfg Config) (*Worker, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if len(cfg.Clients) == 0 {
		return nil, errors.New("al menos un Client requerido")
	}
	if cfg.TickInterval == 0 {
		cfg.TickInterval = DefaultTickInterval
	}
	if cfg.RecheckInterval == 0 {
		cfg.RecheckInterval = DefaultRecheckInterval
	}
	if cfg.Language == "" {
		cfg.Language = "en"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	// Pre-llenamos health con una entrada zero por cada source cableada.
	// Eso hace que el endpoint /admin/sources-health muestre TODAS las
	// sources configuradas desde el día 0 (con last_success_at zero =
	// "todavía no se llamó"), no solo las que ya fallaron o tuvieron éxito.
	health := make(map[string]*SourceHealth, len(cfg.Clients))
	for name := range cfg.Clients {
		health[name] = &SourceHealth{}
	}

	return &Worker{
		db:              cfg.DB,
		clients:         cfg.Clients,
		logger:          cfg.Logger,
		tickInterval:    cfg.TickInterval,
		recheckInterval: cfg.RecheckInterval,
		language:        cfg.Language,
		health:          health,
	}, nil
}

// Health devuelve un snapshot de la salud de cada source. Pensado para
// /admin/sources-health. La copia es defensiva: el caller puede serializar
// sin worry de race con el worker que sigue corriendo.
func (w *Worker) Health() map[string]SourceHealth {
	w.healthMu.Lock()
	defer w.healthMu.Unlock()
	out := make(map[string]SourceHealth, len(w.health))
	for name, h := range w.health {
		out[name] = *h
	}
	return out
}

// recordSuccess y recordError actualizan las stats in-memory de una source.
// Convención: success resetea consecutive_failures; error lo incrementa.
// El last_error sobrescribe siempre — guardamos solo el más reciente.
func (w *Worker) recordSuccess(source string) {
	w.healthMu.Lock()
	defer w.healthMu.Unlock()
	h, ok := w.health[source]
	if !ok {
		h = &SourceHealth{}
		w.health[source] = h
	}
	h.LastSuccessAt = time.Now().UTC()
	h.ConsecutiveFailures = 0
	h.LastError = ""
}

func (w *Worker) recordError(source string, err error) {
	w.healthMu.Lock()
	defer w.healthMu.Unlock()
	h, ok := w.health[source]
	if !ok {
		h = &SourceHealth{}
		w.health[source] = h
	}
	h.LastErrorAt = time.Now().UTC()
	h.ConsecutiveFailures++
	msg := err.Error()
	if len(msg) > maxErrorMessage {
		msg = msg[:maxErrorMessage] + "..."
	}
	h.LastError = msg
}

// Run bloquea hasta que ctx se cancele, corriendo RunOnce con el intervalo
// configurado. Primer tick es inmediato — útil después de un restart para
// retomar filas vencidas sin esperar.
func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("release worker arrancando",
		"tick_interval", w.tickInterval,
		"recheck_interval", w.recheckInterval,
		"language", w.language,
		"sources", w.sourceNames())

	w.tick(ctx)

	t := time.NewTicker(w.tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("release worker apagando")
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) sourceNames() []string {
	out := make([]string, 0, len(w.clients))
	for n := range w.clients {
		out = append(out, n)
	}
	return out
}

func (w *Worker) tick(ctx context.Context) {
	if err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		w.logger.Error("release worker tick", "err", err)
	}
}

// PollOne fetcha y persiste capítulos para un (manga, source) sincrónicamente.
// Expuesto para que library.add (al matchear) dispare un poll inmediato sin
// esperar al próximo tick — sin esto, un manga recién agregado puede tardar
// hasta una hora en mostrar capítulos. Si el source no está en el map de
// clientes (deshabilitada), devuelve un error explícito en vez de silenciarlo.
func (w *Worker) PollOne(ctx context.Context, mangaID int64, source, sourceID string) error {
	client, ok := w.clients[source]
	if !ok {
		return fmt.Errorf("worker: source %q sin cliente cableado", source)
	}
	return w.pollSource(ctx, mangaID, client, sourceID)
}

// RunOnce procesa un batch de filas vencidas. Expuesto para tests y para
// trigger manual.
func (w *Worker) RunOnce(ctx context.Context) error {
	rows, err := w.dueSources(ctx, BatchSize)
	if err != nil {
		return fmt.Errorf("query manga_sources: %w", err)
	}
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		client, ok := w.clients[r.source]
		if !ok {
			// Source deshabilitada: sus filas siguen en DB pero no
			// pollean. Lo logueamos como debug para no spamear.
			w.logger.Debug("source sin cliente cableado; skip",
				"manga_id", r.mangaID, "source", r.source)
			continue
		}
		if err := w.pollSource(ctx, r.mangaID, client, r.sourceID); err != nil {
			w.logger.Warn("poll source",
				"manga_id", r.mangaID,
				"source", r.source,
				"source_id", r.sourceID,
				"err", err)
		}
	}
	return nil
}

type dueSourceRow struct {
	mangaID  int64
	source   string
	sourceID string
}

// dueSources devuelve filas de manga_sources que:
//   - están matcheadas (status='matched')
//   - tienen next_check_at vencido (o NULL = nunca chequeadas)
//   - su manga tiene al menos un library_entry en status=reading
//
// Ordena por next_check_at ascendente (más viejos primero) para que el
// trabajo se distribuya equitativamente.
func (w *Worker) dueSources(ctx context.Context, limit int) ([]dueSourceRow, error) {
	now := time.Now().UTC().Unix()
	rows, err := w.db.QueryContext(ctx, `
		SELECT ms.manga_id, ms.source, ms.source_id
		FROM manga_sources ms
		WHERE ms.status = 'matched'
		  AND ms.source_id != ''
		  AND (ms.next_check_at IS NULL OR ms.next_check_at < ?)
		  AND EXISTS (
		    SELECT 1 FROM library_entries
		    WHERE library_entries.manga_id = ms.manga_id AND library_entries.status = 'reading'
		  )
		ORDER BY ms.next_check_at IS NULL DESC, ms.next_check_at ASC
		LIMIT ?
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []dueSourceRow
	for rows.Next() {
		var r dueSourceRow
		if err := rows.Scan(&r.mangaID, &r.source, &r.sourceID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// pollSource fetch + persist de una (manga, source).
func (w *Worker) pollSource(ctx context.Context, mangaID int64, client releasesource.Client, sourceID string) error {
	source := client.Name()
	since, err := w.latestPublishedAt(ctx, mangaID, source)
	if err != nil {
		return fmt.Errorf("latest published_at: %w", err)
	}

	chapters, err := client.FetchChapters(ctx, sourceID, w.language, since)
	if err != nil {
		// Rate limit no es un error de salud — es backpressure normal.
		// Lo logueamos como tal en lugar de incrementar consecutive_failures.
		if errors.Is(err, releasesource.ErrRateLimited) {
			next := time.Now().Add(RateLimitedDelay).UTC().Unix()
			return w.scheduleNextCheck(ctx, mangaID, source, next)
		}
		w.recordError(source, err)
		return err
	}

	inserted := 0
	for _, ch := range chapters {
		ok, err := w.insertChapter(ctx, mangaID, source, ch)
		if err != nil {
			w.recordError(source, err)
			return fmt.Errorf("insert chapter %q: %w", ch.ID, err)
		}
		if ok {
			inserted++
		}
	}

	next := time.Now().Add(w.recheckInterval).UTC().Unix()
	if err := w.scheduleNextCheck(ctx, mangaID, source, next); err != nil {
		w.recordError(source, err)
		return fmt.Errorf("schedule next: %w", err)
	}
	w.recordSuccess(source)

	if inserted > 0 {
		w.logger.Info("caps nuevos persistidos",
			"manga_id", mangaID,
			"source", source,
			"source_id", sourceID,
			"inserted", inserted)
	}
	return nil
}

// latestPublishedAt devuelve MAX(published_at) + 1s de chapters ya
// persistidos para ese (manga, source) — o zero si nunca polleamos antes.
// El "+1s" evita refetchear el último cap conocido. Filtramos por source
// porque cada source numera/publica independientemente; un cap viejo de
// MangaDex no debe inhibir el primer poll de Asura.
func (w *Worker) latestPublishedAt(ctx context.Context, mangaID int64, source string) (time.Time, error) {
	var ts sql.NullInt64
	err := w.db.QueryRowContext(ctx, `
		SELECT MAX(published_at) FROM chapter WHERE manga_id = ? AND source = ?
	`, mangaID, source).Scan(&ts)
	if err != nil {
		return time.Time{}, err
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return time.Unix(ts.Int64, 0).UTC().Add(time.Second), nil
}

// insertChapter hace upsert idempotente. Devuelve true si fue una fila
// nueva. ON CONFLICT cubre el caso del segundo tick con caps que ya
// persistimos. source viene del Client.Name() — la columna chapter.source
// guarda el origen para que el cursor de lectura distinga sources distintas.
func (w *Worker) insertChapter(ctx context.Context, mangaID int64, source string, ch releasesource.Chapter) (bool, error) {
	now := time.Now().UTC().Unix()
	publishedAt := ch.PublishedAt.UTC().Unix()
	res, err := w.db.ExecContext(ctx, `
		INSERT INTO chapter (manga_id, source, source_id, number, title, language, published_at, external_url, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, source_id) DO NOTHING
	`,
		mangaID, source, ch.ID,
		nullIfEmpty(ch.Number), nullIfEmpty(ch.Title),
		nullIfEmpty(ch.Language), publishedAt,
		nullIfEmpty(ch.ExternalURL), now,
	)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

func (w *Worker) scheduleNextCheck(ctx context.Context, mangaID int64, source string, nextTs int64) error {
	now := time.Now().UTC().Unix()
	_, err := w.db.ExecContext(ctx, `
		UPDATE manga_sources SET next_check_at = ?, checked_at = ?
		WHERE manga_id = ? AND source = ?
	`, nextTs, now, mangaID, source)
	return err
}

// nullIfEmpty pasa nil al driver SQLite cuando el string es vacío, así
// las columnas TEXT nullable quedan NULL en lugar de "" — más limpio
// para queries y para el export de data.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
