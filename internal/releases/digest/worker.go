package digest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// WorkerConfig configura el worker semanal.
type WorkerConfig struct {
	DB     *sql.DB
	Sender *Sender
	Logger *slog.Logger
	// Tick controla la frecuencia del check. Default: 1h. Sobreescribible
	// solo en tests.
	Tick time.Duration
	// FireDay y FireHourStart acotan la ventana de envío. Default: domingo
	// 9-22 UTC. Cualquier tick dentro de la ventana es elegible para
	// disparar — la tabla digest_runs evita duplicados y permite recovery
	// si el binario se reinició fuera de la franja inicial.
	FireDay       time.Weekday
	FireHourStart int
	FireHourEnd   int // inclusive
	// MaxRunsPerDay acota cuántos intentos de dispatch hace el worker en
	// el mismo día. Default: 3 (uno inicial + 2 retries). Si SMTP cae por
	// largo rato, evita thrash de logs.
	MaxRunsPerDay int
}

// Worker corre el digest semanal: tickea cada Tick. En cada tick, si
// estamos dentro de la ventana (FireDay, FireHourStart..FireHourEnd UTC),
// consulta digest_runs del día y decide si:
//   - dispatch nuevo (no hubo runs hoy), o
//   - retry diferido (último run del día tiene failed>0, debajo del cap), o
//   - skip (último run del día completó sin failures, o ya alcanzamos el cap).
//
// La idempotencia por user la sigue garantizando el Sender vía
// users.last_digest_sent_at — usuarios que ya recibieron el digest hoy se
// saltan automáticamente en el segundo intento.
type Worker struct {
	db            *sql.DB
	sender        *Sender
	logger        *slog.Logger
	tick          time.Duration
	fireDay       time.Weekday
	fireHourStart int
	fireHourEnd   int
	maxRunsPerDay int
	now           func() time.Time // inyectable para tests
}

// NewWorker construye un Worker. Falla si dependencias críticas faltan.
func NewWorker(cfg WorkerConfig) (*Worker, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Sender == nil {
		return nil, errors.New("Sender requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Tick == 0 {
		cfg.Tick = 1 * time.Hour
	}
	// Default: domingo 9-22 UTC, max 3 dispatches. Detectamos "todo en
	// cero" como pedido de defaults; un caller que necesita lunes 00:00
	// debe setear explícitamente FireHourEnd>=0 (zero value de int).
	if cfg.FireDay == 0 && cfg.FireHourStart == 0 && cfg.FireHourEnd == 0 {
		cfg.FireDay = time.Sunday
		cfg.FireHourStart = 9
		cfg.FireHourEnd = 22
	}
	if cfg.MaxRunsPerDay == 0 {
		cfg.MaxRunsPerDay = 3
	}
	return &Worker{
		db:            cfg.DB,
		sender:        cfg.Sender,
		logger:        cfg.Logger,
		tick:          cfg.Tick,
		fireDay:       cfg.FireDay,
		fireHourStart: cfg.FireHourStart,
		fireHourEnd:   cfg.FireHourEnd,
		maxRunsPerDay: cfg.MaxRunsPerDay,
		now:           time.Now,
	}, nil
}

// Run bloquea hasta ctx.Done(). Hace un primer chequeo inmediato para no
// perder la ventana si el binario arrancó adentro del slot, después tickea
// cada Tick.
func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("digest worker iniciado",
		"tick", w.tick,
		"fire", w.fireDay.String()+" "+itoaHour(w.fireHourStart)+"-"+itoaHour(w.fireHourEnd)+" UTC",
		"max_runs_per_day", w.maxRunsPerDay,
	)

	w.tickIfDue(ctx)

	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("digest worker detenido")
			return
		case <-t.C:
			w.tickIfDue(ctx)
		}
	}
}

// tickIfDue decide si disparar un dispatch en este tick.
//
// Ventana: dentro de FireDay y FireHourStart <= Hour <= FireHourEnd UTC.
//
// Dentro de la ventana consulta digest_runs del día (UTC). Tres casos:
//   - sin runs hoy: primer intento del día → dispatch.
//   - último run tiene failed>0 y aún no alcanzamos MaxRunsPerDay: retry diferido.
//   - último run completó sin failures, o ya estamos en el cap: skip.
func (w *Worker) tickIfDue(ctx context.Context) {
	now := w.now().UTC()
	if now.Weekday() != w.fireDay || now.Hour() < w.fireHourStart || now.Hour() > w.fireHourEnd {
		return
	}

	startOfDay := startOfUTCDay(now).Unix()
	runsToday, lastFailed, err := w.runsToday(ctx, startOfDay)
	if err != nil {
		w.logger.Error("consultar digest_runs", "err", err)
		return
	}
	switch {
	case runsToday == 0:
		// Primer intento del día.
	case runsToday >= w.maxRunsPerDay:
		// Cap alcanzado. Aunque haya failures, no thrashemos los logs
		// — el reintento real será la semana siguiente vía el cutoff
		// no avanzado en users.last_digest_sent_at.
		return
	case lastFailed == 0:
		// El último intento del día terminó limpio. No hay nada que reintentar.
		return
	default:
		// runsToday>=1 && lastFailed>0 && runsToday<cap → retry diferido.
		w.logger.Info("digest worker reintentando run con failures previas", "runs_hoy", runsToday)
	}

	if err := w.dispatchAll(ctx); err != nil {
		w.logger.Error("dispatch digest", "err", err)
	}
}

// runsToday devuelve cuántos digest_runs se iniciaron hoy y el counter
// `failed` del último (0 si no hay runs).
func (w *Worker) runsToday(ctx context.Context, startOfDay int64) (count int, lastFailed int, err error) {
	err = w.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE((SELECT failed FROM digest_runs
		                 WHERE started_at >= ?
		                 ORDER BY id DESC LIMIT 1), 0)
		FROM digest_runs
		WHERE started_at >= ?
	`, startOfDay, startOfDay).Scan(&count, &lastFailed)
	return count, lastFailed, err
}

// dispatchAll itera sobre usuarios opt-in y dispara SendToUser por cada uno.
// Inserta una fila en digest_runs al inicio y la actualiza con los counters
// al final. Errores individuales se loguean pero no abortan la pasada — un
// user con SMTP failing no debe bloquear los demás. Los users que ya
// recibieron el digest hoy (last_digest_sent_at >= startOfDay) se filtran
// en SQL para que los retries diferidos solo intenten los pendientes.
func (w *Worker) dispatchAll(ctx context.Context) error {
	startedAt := w.now().UTC().Unix()
	startOfDay := startOfUTCDay(w.now().UTC()).Unix()

	res, err := w.db.ExecContext(ctx,
		`INSERT INTO digest_runs (started_at) VALUES (?)`, startedAt)
	if err != nil {
		return fmt.Errorf("insert digest_runs: %w", err)
	}
	runID, _ := res.LastInsertId()

	rows, err := w.db.QueryContext(ctx, `
		SELECT id FROM users
		WHERE email_digest_enabled = 1
		  AND deleted_at IS NULL
		  AND (last_digest_sent_at IS NULL OR last_digest_sent_at < ?)
	`, startOfDay)
	if err != nil {
		return err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	w.logger.Info("digest worker disparando", "candidatos", len(ids), "run_id", runID)

	sent, skipped, failed := 0, 0, 0
	for _, id := range ids {
		switch err := w.sender.SendToUser(ctx, id); {
		case err == nil:
			sent++
		case errors.Is(err, ErrNoNewChapters):
			skipped++
		default:
			failed++
			w.logger.Error("enviar digest", "user_id", id, "err", err)
		}
	}

	if _, err := w.db.ExecContext(ctx, `
		UPDATE digest_runs
		SET finished_at = ?, candidates = ?, sent = ?, skipped = ?, failed = ?
		WHERE id = ?
	`, w.now().UTC().Unix(), len(ids), sent, skipped, failed, runID); err != nil {
		w.logger.Error("update digest_runs", "run_id", runID, "err", err)
	}

	// Log nivel apropiado al resultado: ERROR si algún user falló (visible
	// en alertas), INFO si todo limpio.
	logFn := w.logger.Info
	if failed > 0 {
		logFn = w.logger.Error
	}
	logFn("digest worker terminó",
		"run_id", runID, "sent", sent, "skipped", skipped, "failed", failed)
	return nil
}

// startOfUTCDay devuelve 00:00:00 UTC del día de t.
func startOfUTCDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// itoaHour formatea un hour 0..23 con padding "0X" para HH:00 visual.
func itoaHour(h int) string {
	if h < 10 {
		return "0" + string(rune('0'+h))
	}
	tens, units := h/10, h%10
	return string(rune('0'+tens)) + string(rune('0'+units))
}
