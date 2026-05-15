package digest

import (
	"context"
	"database/sql"
	"errors"
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
	// FireDay y FireHour acotan la ventana de envío. Default: domingo 9am UTC.
	// El tick que cae dentro de la hora de FireHour ese día dispara una
	// pasada; los demás ticks son no-ops.
	FireDay  time.Weekday
	FireHour int
}

// Worker corre el digest semanal: tickea cada Tick, y cuando la hora UTC
// cae dentro de la ventana (FireDay, FireHour..FireHour+1), itera sobre los
// usuarios opt-in y dispara Sender.SendToUser por cada uno. La idempotencia
// la garantiza el Sender (users.last_digest_sent_at).
type Worker struct {
	db       *sql.DB
	sender   *Sender
	logger   *slog.Logger
	tick     time.Duration
	fireDay  time.Weekday
	fireHour int
	now      func() time.Time // inyectable para tests
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
	if cfg.FireHour == 0 && cfg.FireDay == 0 {
		cfg.FireDay = time.Sunday
		cfg.FireHour = 9
	}
	return &Worker{
		db:       cfg.DB,
		sender:   cfg.Sender,
		logger:   cfg.Logger,
		tick:     cfg.Tick,
		fireDay:  cfg.FireDay,
		fireHour: cfg.FireHour,
		now:      time.Now,
	}, nil
}

// Run bloquea hasta ctx.Done(). Hace un primer chequeo inmediato para no
// perder la ventana si el binario arrancó adentro del slot, después tickea
// cada Tick.
func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("digest worker iniciado", "tick", w.tick, "fire", w.fireDay.String()+" "+itoaHour(w.fireHour)+":00 UTC")

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

// tickIfDue dispara una pasada si la hora UTC está en la ventana configurada.
// Fuera de la ventana es no-op silencioso.
func (w *Worker) tickIfDue(ctx context.Context) {
	now := w.now().UTC()
	if now.Weekday() != w.fireDay || now.Hour() != w.fireHour {
		return
	}
	if err := w.dispatchAll(ctx); err != nil {
		w.logger.Error("dispatch digest", "err", err)
	}
}

// dispatchAll itera sobre usuarios opt-in y dispara SendToUser por cada uno.
// Errores individuales se loguean pero no abortan la pasada — un user con
// SMTP failing no debe bloquear los demás.
func (w *Worker) dispatchAll(ctx context.Context) error {
	rows, err := w.db.QueryContext(ctx, `
		SELECT id FROM users
		WHERE email_digest_enabled = 1
		  AND deleted_at IS NULL
	`)
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

	w.logger.Info("digest worker disparando", "candidatos", len(ids))

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
	w.logger.Info("digest worker terminó", "sent", sent, "skipped", skipped, "failed", failed)
	return nil
}

// itoaHour formatea un hour 0..23 con padding "0X" para HH:00 visual.
func itoaHour(h int) string {
	if h < 10 {
		return "0" + string(rune('0'+h))
	}
	tens, units := h/10, h%10
	return string(rune('0'+tens)) + string(rune('0'+units))
}
