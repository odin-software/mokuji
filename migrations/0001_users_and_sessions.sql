-- 0001_users_and_sessions.sql
--
-- Tablas base: usuarios identificados por email + sesiones server-side
-- con token CSRF asociado.
--
-- Decisiones aplicadas:
--   users.email UNIQUE  -> una cuenta por email (ADR 0004)
--   sessions.id_hash    -> SHA-256 hex del session ID (ADR 0005)
--   sessions.csrf_token -> random 256-bit en hex (ADR 0006)
--   ON DELETE CASCADE   -> borrar usuario limpia sus sesiones

CREATE TABLE users (
    id         INTEGER PRIMARY KEY,
    email      TEXT    NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
);

CREATE TABLE sessions (
    id_hash      TEXT    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token   TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    user_agent   TEXT,
    ip_address   TEXT
);

CREATE INDEX idx_sessions_user    ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
