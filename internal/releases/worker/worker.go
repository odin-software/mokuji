// Package worker corre el job en background que polleea MangaDex por
// capítulos nuevos de los mangas matcheados que algún usuario tiene en
// status=reading. Diseño completo en ADR 0013 §5.
//
// Una goroutine única dentro del proceso de mokuji, schedule con
// time.Ticker. En cada tick, query SQL devuelve hasta BatchSize mangas
// cuyo mangadex_next_check_at venció, y por cada uno se hace 1 request a
// /manga/{id}/feed (filtrado por publishAtSince con el último cap conocido
// para no traer histórico cada vez). Caps nuevos se persisten en la tabla
// chapter; next_check_at se setea a now+RecheckInterval.
//
// Cola implícita en la tabla manga (mangadex_next_check_at) — sin tabla
// de jobs aparte.
package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"git.odin.do/kengru/mokuji/internal/mangadex"
)

// Defaults conservadores. En producción se overridean vía Config.
const (
	DefaultTickInterval    = 1 * time.Hour
	DefaultRecheckInterval = 6 * time.Hour
	// RateLimitedDelay es cuánto esperamos antes de retomar un manga que
	// nos devolvió ErrRateLimited. Más que el tick para que no monopolice.
	RateLimitedDelay = 15 * time.Minute
	// BatchSize es cuántos mangas procesamos por tick. Cota para no
	// bloquear demasiado tiempo en el loop si la cola creció.
	BatchSize = 50
)

// FeedFetcher es lo mínimo que necesitamos del cliente MangaDex.
// Inyectable para tests.
type FeedFetcher interface {
	FetchFeed(ctx context.Context, mangaID, language string, since time.Time) ([]mangadex.Chapter, error)
}

// Config agrupa las dependencias.
type Config struct {
	DB              *sql.DB
	Client          FeedFetcher
	Logger          *slog.Logger
	TickInterval    time.Duration // 0 -> DefaultTickInterval
	RecheckInterval time.Duration // 0 -> DefaultRecheckInterval
	Language        string        // 0 -> "en"
}

// Worker es el job en background.
type Worker struct {
	db              *sql.DB
	client          FeedFetcher
	logger          *slog.Logger
	tickInterval    time.Duration
	recheckInterval time.Duration
	language        string
}

// New construye un Worker con defaults razonables.
func New(cfg Config) (*Worker, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Client == nil {
		return nil, errors.New("Client requerido")
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
	return &Worker{
		db:              cfg.DB,
		client:          cfg.Client,
		logger:          cfg.Logger,
		tickInterval:    cfg.TickInterval,
		recheckInterval: cfg.RecheckInterval,
		language:        cfg.Language,
	}, nil
}

// Run bloquea hasta que ctx se cancele, corriendo RunOnce con el intervalo
// configurado. Primer tick es inmediato — útil después de un restart para
// retomar mangas vencidos sin esperar.
func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("release worker arrancando",
		"tick_interval", w.tickInterval,
		"recheck_interval", w.recheckInterval,
		"language", w.language)

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

func (w *Worker) tick(ctx context.Context) {
	if err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		w.logger.Error("release worker tick", "err", err)
	}
}

// PollOne fetches y persiste capítulos para un solo manga sincrónicamente.
// Expuesto para que library.add (al matchear) dispare un poll inmediato sin
// esperar al próximo tick — sin esto, un manga recién agregado puede tardar
// hasta una hora en mostrar capítulos. Retorna el error directo del FetchFeed
// (incluyendo mangadex.ErrRateLimited cuando aplique) para que el caller
// decida si lo logguea como warn o aborta.
func (w *Worker) PollOne(ctx context.Context, mangaID int64, mangadexID string) error {
	return w.pollManga(ctx, mangaID, mangadexID)
}

// RunOnce procesa un batch de mangas vencidos. Expuesto para tests y para
// trigger manual (eventualmente vía endpoint admin si hace falta).
func (w *Worker) RunOnce(ctx context.Context) error {
	mangas, err := w.dueMangas(ctx, BatchSize)
	if err != nil {
		return fmt.Errorf("query mangas: %w", err)
	}
	for _, m := range mangas {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.pollManga(ctx, m.id, m.mangadexID); err != nil {
			// Loguear y seguir con los demás. Un manga roto no debe
			// detener el tick.
			w.logger.Warn("poll manga",
				"manga_id", m.id,
				"mangadex_id", m.mangadexID,
				"err", err)
		}
	}
	return nil
}

type dueMangaRow struct {
	id         int64
	mangadexID string
}

// dueMangas devuelve mangas que:
//   - están matcheados con MangaDex (mangadex_id IS NOT NULL, status=matched)
//   - tienen mangadex_next_check_at vencido (o NULL = nunca chequeados)
//   - tienen al menos un library_entry en status=reading
//
// Ordena por next_check_at ascendente (más viejos primero) para que el
// trabajo se distribuya equitativamente.
func (w *Worker) dueMangas(ctx context.Context, limit int) ([]dueMangaRow, error) {
	now := time.Now().UTC().Unix()
	rows, err := w.db.QueryContext(ctx, `
		SELECT manga.id, manga.mangadex_id
		FROM manga
		WHERE manga.mangadex_id IS NOT NULL
		  AND manga.mangadex_status = 'matched'
		  AND (manga.mangadex_next_check_at IS NULL OR manga.mangadex_next_check_at < ?)
		  AND EXISTS (
		    SELECT 1 FROM library_entries
		    WHERE library_entries.manga_id = manga.id AND library_entries.status = 'reading'
		  )
		ORDER BY manga.mangadex_next_check_at IS NULL DESC, manga.mangadex_next_check_at ASC
		LIMIT ?
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []dueMangaRow
	for rows.Next() {
		var r dueMangaRow
		if err := rows.Scan(&r.id, &r.mangadexID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// pollManga fetch + persist de un manga.
func (w *Worker) pollManga(ctx context.Context, mangaID int64, mangadexID string) error {
	since, err := w.latestPublishedAt(ctx, mangaID)
	if err != nil {
		return fmt.Errorf("latest published_at: %w", err)
	}

	chapters, err := w.client.FetchFeed(ctx, mangadexID, w.language, since)
	if err != nil {
		if errors.Is(err, mangadex.ErrRateLimited) {
			// MangaDex apretó temporalmente. Reagendar un poco más tarde
			// del tick normal para no insistir inmediatamente.
			next := time.Now().Add(RateLimitedDelay).UTC().Unix()
			return w.scheduleNextCheck(ctx, mangaID, next)
		}
		return err
	}

	inserted := 0
	for _, ch := range chapters {
		ok, err := w.insertChapter(ctx, mangaID, ch)
		if err != nil {
			return fmt.Errorf("insert chapter %q: %w", ch.ID, err)
		}
		if ok {
			inserted++
		}
	}

	next := time.Now().Add(w.recheckInterval).UTC().Unix()
	if err := w.scheduleNextCheck(ctx, mangaID, next); err != nil {
		return fmt.Errorf("schedule next: %w", err)
	}

	if inserted > 0 {
		w.logger.Info("caps nuevos persistidos",
			"manga_id", mangaID,
			"mangadex_id", mangadexID,
			"inserted", inserted)
	}
	return nil
}

// latestPublishedAt devuelve MAX(published_at) + 1s de chapters ya
// persistidos para ese manga (o zero si nunca polleamos antes). El "+1s"
// evita refetchear el último cap conocido.
func (w *Worker) latestPublishedAt(ctx context.Context, mangaID int64) (time.Time, error) {
	var ts sql.NullInt64
	err := w.db.QueryRowContext(ctx, `
		SELECT MAX(published_at) FROM chapter WHERE manga_id = ?
	`, mangaID).Scan(&ts)
	if err != nil {
		return time.Time{}, err
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return time.Unix(ts.Int64, 0).UTC().Add(time.Second), nil
}

// insertChapter hace upsert idempotente. Devuelve true si fue una fila
// nueva (no si ya existía y se ignoró). ON CONFLICT cubre el caso del
// segundo tick con caps que ya persistimos.
func (w *Worker) insertChapter(ctx context.Context, mangaID int64, ch mangadex.Chapter) (bool, error) {
	now := time.Now().UTC().Unix()
	publishedAt := ch.PublishedAt.UTC().Unix()
	res, err := w.db.ExecContext(ctx, `
		INSERT INTO chapter (manga_id, source, source_id, number, title, language, published_at, external_url, created_at)
		VALUES (?, 'mangadex', ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, source_id) DO NOTHING
	`,
		mangaID, ch.ID,
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

func (w *Worker) scheduleNextCheck(ctx context.Context, mangaID, nextTs int64) error {
	now := time.Now().UTC().Unix()
	_, err := w.db.ExecContext(ctx, `
		UPDATE manga SET mangadex_next_check_at = ?, mangadex_checked_at = ?
		WHERE id = ?
	`, nextTs, now, mangaID)
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
