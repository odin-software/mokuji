package profile

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// GracePeriod es la ventana entre soft delete (users.deleted_at != NULL) y
// hard delete (DELETE FROM users + entry en deletion_log). En ese plazo el
// user puede cancelar el borrado via /restore-account/cancel.
const GracePeriod = 7 * 24 * time.Hour

// HardDeleteUser borra los datos del user de forma permanente, dejando solo
// una entrada en deletion_log con un hash one-way del user_id. Es la única
// operación destructiva del package — la usan tanto el worker periódico
// (después de la gracia) como el flujo "borrar ya" en /restore-account.
//
// Pepper es un secret server-side que se concatena al user_id antes de
// hashear, para que un atacante con acceso a deletion_log no pueda enumerar
// IDs probando hashes. Vacío = inseguro; se loguea warn pero no falla.
//
// Las CASCADEs en library_entries y deletion_codes cubren la limpieza de
// data dependiente. sessions también tiene CASCADE; otp_codes está atada a
// email, no a user_id, así que la borramos explícitamente.
func HardDeleteUser(ctx context.Context, db *sql.DB, userID int64, pepper string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Cargar el email antes del DELETE para limpiar otp_codes (sin FK).
	var emailAddr string
	err = tx.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, userID).Scan(&emailAddr)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("user no existe")
	}
	if err != nil {
		return fmt.Errorf("cargar user: %w", err)
	}

	now := time.Now().UTC().Unix()
	hash := hashUserID(userID, pepper)

	// Insertar primero en el log (idempotente por PK). Si la inserción
	// falla, abortamos antes de borrar la data — preferimos no borrar a
	// borrar sin trazabilidad.
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO deletion_log (user_id_hash, deleted_at)
		VALUES (?, ?)
	`, hash, now); err != nil {
		return fmt.Errorf("insertar deletion_log: %w", err)
	}

	// otp_codes está atada a email, no a user_id; limpiar explícito.
	if _, err := tx.ExecContext(ctx, `DELETE FROM otp_codes WHERE email = ?`, emailAddr); err != nil {
		return fmt.Errorf("borrar otp_codes: %w", err)
	}

	// DELETE FROM users dispara CASCADE en library_entries, sessions y
	// deletion_codes (todas atadas via user_id).
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID); err != nil {
		return fmt.Errorf("borrar user: %w", err)
	}
	return tx.Commit()
}

// hashUserID = SHA-256(user_id || ":" || pepper). El separador evita
// colisiones tipo (userID=12, pepper="3X") vs (userID=1, pepper="23X").
func hashUserID(userID int64, pepper string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s", userID, pepper)
	return hex.EncodeToString(h.Sum(nil))
}

// CleanupWorker corre el hard delete diario: busca users con
// deleted_at + GracePeriod < now y los borra. También limpia deletion_codes
// huérfanos (expirados hace > 1 día) — no es crítico pero es housekeeping.
type CleanupWorker struct {
	db     *sql.DB
	pepper string
	logger *slog.Logger
	tick   time.Duration
	now    func() time.Time
}

// CleanupWorkerConfig configura el worker.
type CleanupWorkerConfig struct {
	DB     *sql.DB
	Pepper string
	Logger *slog.Logger
	// Tick controla la frecuencia del check. Default: 24h. Sobreescribible
	// solo en tests.
	Tick time.Duration
}

// NewCleanupWorker construye un worker. Falla si DB es nil. Pepper vacío
// loguea warn pero permite arrancar (degradación, no hard fail).
func NewCleanupWorker(cfg CleanupWorkerConfig) (*CleanupWorker, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Tick == 0 {
		cfg.Tick = 24 * time.Hour
	}
	if cfg.Pepper == "" {
		cfg.Logger.Warn("DELETION_LOG_PEPPER vacío: los hashes del audit log son enumerables")
	}
	return &CleanupWorker{
		db:     cfg.DB,
		pepper: cfg.Pepper,
		logger: cfg.Logger,
		tick:   cfg.Tick,
		now:    time.Now,
	}, nil
}

// Run bloquea hasta ctx.Done(). Tickea cada Tick; en cada tick borra users
// con deleted_at + GracePeriod < now.
func (w *CleanupWorker) Run(ctx context.Context) {
	w.logger.Info("cleanup worker iniciado", "tick", w.tick, "grace", GracePeriod)
	w.tickOnce(ctx)

	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("cleanup worker detenido")
			return
		case <-t.C:
			w.tickOnce(ctx)
		}
	}
}

func (w *CleanupWorker) tickOnce(ctx context.Context) {
	cutoff := w.now().UTC().Add(-GracePeriod).Unix()

	rows, err := w.db.QueryContext(ctx, `
		SELECT id FROM users WHERE deleted_at IS NOT NULL AND deleted_at < ?
	`, cutoff)
	if err != nil {
		w.logger.Error("cleanup query", "err", err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			w.logger.Error("cleanup scan", "err", err)
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	rows.Close()

	if len(ids) > 0 {
		w.logger.Info("cleanup: hard delete", "candidatos", len(ids))
		for _, id := range ids {
			if err := HardDeleteUser(ctx, w.db, id, w.pepper); err != nil {
				w.logger.Error("cleanup: hard delete falló", "user_id", id, "err", err)
			}
		}
	}

	// Housekeeping: códigos expirados hace > 1 día se borran. No es crítico
	// (el consume ya los rechaza) pero evita que la tabla crezca sin tope.
	// Corre siempre (sin importar si hubo hard delete) — independiente.
	staleCutoff := w.now().UTC().Add(-24 * time.Hour).Unix()
	if _, err := w.db.ExecContext(ctx,
		`DELETE FROM deletion_codes WHERE expires_at < ?`, staleCutoff,
	); err != nil {
		w.logger.Warn("cleanup deletion_codes viejos", "err", err)
	}
}
