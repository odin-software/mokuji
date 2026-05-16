-- Migration 0006: códigos de confirmación para borrar cuenta.
--
-- Tabla separada de otp_codes para evitar que un código de login se reutilice
-- para borrar (scopes distintos). Atado a user_id (no email) porque el user
-- ya está autenticado al pedir el código — sabemos quién es.
CREATE TABLE deletion_codes (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX idx_deletion_codes_user ON deletion_codes(user_id);
