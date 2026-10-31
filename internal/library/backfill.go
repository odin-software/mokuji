package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/odin-software/mokuji/internal/releases/matcher"
)

// Backfiller corre un job único al arranque que rellena manga_sources
// para combinaciones (manga en biblioteca, source configurada) que no
// tienen fila todavía.
//
// Caso de uso: el operador habilita una nueva source (ej. ASURA_ENABLED=true)
// después de un deploy. Sin backfill, los mangas que ya estaban en
// biblioteca quedan sin entry en manga_sources para esa source — el user
// tendría que entrar al detail page de cada uno y darle "reintentar". Con
// backfill, mokuji lo hace automáticamente en background al arrancar.
//
// Diseño:
//   - Fire-and-forget desde main; nunca bloquea el arranque del server
//   - Heredamos el rate limiter del cliente de cada source — no hace falta
//     una capa de throttling extra
//   - Idempotente: si ya hay fila para (manga, source), skip silencioso
//   - Bounded: el query devuelve hasta BackfillBatchSize por iteración
//     para no monopolizar la DB; varias iteraciones cubren toda la cola
//   - Solo procesa mangas que tienen al menos un library_entry status='reading'
//     — manga abandonado/completado no es prioritario

// BackfillBatchSize es cuántas combinaciones (manga, source faltante) se
// procesan por iteración antes de re-querear la cola. Pequeño para no
// monopolizar la conn de SQL.
const BackfillBatchSize = 25

// BackfillConfig agrupa las dependencias del Backfiller.
type BackfillConfig struct {
	DB             *sql.DB
	SourceMatchers map[string]Matcher
	Logger         *slog.Logger
}

// Backfiller es el job de relleno.
type Backfiller struct {
	db       *sql.DB
	matchers map[string]Matcher
	logger   *slog.Logger
}

// NewBackfiller construye el job. Falla si no hay matchers cableados —
// no hay nada que backfillear.
func NewBackfiller(cfg BackfillConfig) (*Backfiller, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if len(cfg.SourceMatchers) == 0 {
		return nil, errors.New("al menos un matcher requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Backfiller{
		db:       cfg.DB,
		matchers: cfg.SourceMatchers,
		logger:   cfg.Logger,
	}, nil
}

// Run procesa la cola hasta vaciarla o hasta que ctx se cancele.
// Pensado para invocarse en una goroutine al arranque; loguea progreso
// pero nunca devuelve error fatal (los errores per-manga son warnings).
func (b *Backfiller) Run(ctx context.Context) {
	b.logger.Info("backfill multi-source arrancando", "sources", b.sourceNames())
	start := time.Now()
	processed := 0
	for {
		if err := ctx.Err(); err != nil {
			b.logger.Info("backfill cancelado", "processed", processed, "elapsed", time.Since(start))
			return
		}
		n, err := b.runBatch(ctx)
		if err != nil {
			b.logger.Error("backfill batch", "err", err)
			return
		}
		processed += n
		if n == 0 {
			b.logger.Info("backfill terminó", "processed", processed, "elapsed", time.Since(start))
			return
		}
	}
}

func (b *Backfiller) sourceNames() []string {
	out := make([]string, 0, len(b.matchers))
	for n := range b.matchers {
		out = append(out, n)
	}
	return out
}

// runBatch toma hasta BackfillBatchSize mangas que carecen de fila para
// alguna source configurada, e intenta matchearlos. Devuelve cuántas
// combinaciones (manga, source) procesó. 0 = cola vacía.
//
// El query: por cada source configurada que NO tiene fila para el manga
// y el manga está en reading, listamos (manga_id, title, source). El
// loop hace una llamada al matcher y un upsert por combinación.
func (b *Backfiller) runBatch(ctx context.Context) (int, error) {
	tasks, err := b.dueTasks(ctx, BackfillBatchSize)
	if err != nil {
		return 0, fmt.Errorf("query tasks: %w", err)
	}
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		b.processOne(ctx, task)
	}
	return len(tasks), nil
}

type backfillTask struct {
	mangaID int64
	title   string
	source  string
}

// dueTasks busca mangas (que tienen al menos un reading entry) sin fila
// en manga_sources para alguna source configurada. Una combinación
// (manga, source) por task. Bound por limit total.
func (b *Backfiller) dueTasks(ctx context.Context, limit int) ([]backfillTask, error) {
	// Construimos un VALUES literal con los nombres de sources para
	// "cross join" — SQLite no tiene UNNEST. Sources son strings que
	// validamos en el config; isValidSourceName chequea que vienen de
	// código (no de user input) pero ya las controlamos.
	if len(b.matchers) == 0 {
		return nil, nil
	}
	values := make([]string, 0, len(b.matchers))
	args := make([]any, 0, len(b.matchers)+1)
	for name := range b.matchers {
		values = append(values, "(?)")
		args = append(args, name)
	}
	args = append(args, limit)

	q := fmt.Sprintf(`
		WITH configured(source) AS (VALUES %s)
		SELECT m.id, m.title, configured.source
		FROM manga m
		JOIN library_entries le ON le.manga_id = m.id AND le.status = 'reading'
		CROSS JOIN configured
		LEFT JOIN manga_sources ms
		  ON ms.manga_id = m.id AND ms.source = configured.source
		WHERE ms.manga_id IS NULL
		GROUP BY m.id, configured.source
		LIMIT ?
	`, strings.Join(values, ","))

	rows, err := b.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []backfillTask
	for rows.Next() {
		var t backfillTask
		if err := rows.Scan(&t.mangaID, &t.title, &t.source); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// processOne corre el matcher y upserta el resultado. Errores se
// loguean como warn pero no abortan el batch — el siguiente runBatch
// retoma porque la fila sigue sin existir.
//
// Si el matcher devuelve `unmatched` (caso raro: ningún input no-vacío)
// upsertamos igualmente para no quedar en loop infinito retomando la
// misma combinación.
func (b *Backfiller) processOne(ctx context.Context, task backfillTask) {
	m, ok := b.matchers[task.source]
	if !ok {
		// Source desapareció entre el query y aquí (deshabilitada
		// mid-arranque, raro). Skip.
		return
	}
	result, err := m.Match(ctx, task.title)
	if err != nil {
		b.logger.Warn("backfill match",
			"manga_id", task.mangaID, "source", task.source, "title", task.title, "err", err)
		// Insertamos placeholder con status=unmatched para que el
		// próximo runBatch no lo retome — el worker periódico (o user)
		// puede reintentar.
		_ = b.upsert(ctx, task.mangaID, task.source, string(matcher.StatusUnmatched), "")
		return
	}
	var sourceID string
	if result.Status == matcher.StatusMatched {
		sourceID = result.SourceID
	}
	if err := b.upsert(ctx, task.mangaID, task.source, string(result.Status), sourceID); err != nil {
		b.logger.Error("backfill upsert",
			"manga_id", task.mangaID, "source", task.source, "err", err)
	}
}

func (b *Backfiller) upsert(ctx context.Context, mangaID int64, source, status, sourceID string) error {
	_, err := b.db.ExecContext(ctx, `
		INSERT INTO manga_sources (manga_id, source, source_id, status, checked_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (manga_id, source) DO UPDATE
		SET source_id = excluded.source_id,
		    status    = excluded.status,
		    checked_at = excluded.checked_at
	`, mangaID, source, sourceID, status, time.Now().UTC().Unix())
	return err
}

