-- 0010_digest_runs.sql
--
-- Tabla de auditoría del worker de digest semanal. Una fila por cada
-- pasada de dispatchAll. Resuelve dos problemas operativos:
--
-- (1) Recovery de ventana perdida. Antes el worker solo disparaba si el
--     tick caía exactamente en Hour==9 UTC del domingo. Reiniciar el
--     binario entre las 10:00 UTC y el próximo lunes significaba perder
--     el digest de esa semana sin alerta. Ahora la ventana es Sunday
--     9-22 UTC, y el worker consulta esta tabla para no re-disparar si
--     ya hubo un run exitoso del día.
--
-- (2) Retry diferido en caso de SMTP transient. Si un run terminó con
--     failed>0, el siguiente tick dentro de la ventana puede reintentar
--     (cap de 3 runs por día para evitar thrash si SMTP cae completo).
--     Los users con last_digest_sent_at avanzado no se re-procesan.
--
-- finished_at NULL = run abortado mid-loop (proceso crasheó). Útil para
-- detectar problemas serios distintos de un fallo SMTP por user.

CREATE TABLE digest_runs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at   INTEGER NOT NULL,           -- unix seconds, UTC
  finished_at  INTEGER,                    -- unix seconds; NULL si crash mid-run
  candidates   INTEGER NOT NULL DEFAULT 0, -- users opt-in considerados
  sent         INTEGER NOT NULL DEFAULT 0,
  skipped      INTEGER NOT NULL DEFAULT 0, -- ErrNoNewChapters
  failed       INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_digest_runs_started ON digest_runs(started_at DESC);
