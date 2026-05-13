-- 0002_otp_codes.sql
--
-- Tabla de códigos OTP pendientes (ADR 0004).
--
-- Decisiones aplicadas:
--   code_hash    -> SHA-256 hex del código; nunca el plaintext en DB
--   email        -> no FK a users porque el usuario se crea lazy al verificar
--   used_at      -> marca consumo single-use; NULL si aún no se usó
--   ip_address y user_agent -> contexto para mostrar al usuario en el email

CREATE TABLE otp_codes (
    id          INTEGER PRIMARY KEY,
    email       TEXT    NOT NULL,
    code_hash   TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    used_at     INTEGER,
    ip_address  TEXT,
    user_agent  TEXT
);

CREATE INDEX idx_otp_email   ON otp_codes(email);
CREATE INDEX idx_otp_expires ON otp_codes(expires_at);
