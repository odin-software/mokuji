-- 0013_admin_grants.sql
--
-- Tokens de un solo uso para recuperación de acceso administrativa
-- (break-glass): los emite POST /admin/grant-session y los canjea
-- GET /auth/grant por una sesión real.
--
-- Decisiones aplicadas:
--   token_hash  -> SHA-256 hex del token; nunca el plaintext en DB, igual
--                  que otp_codes.code_hash y sessions.id_hash
--   user_id FK  -> a diferencia de otp_codes, acá sí hay FK: el endpoint
--                  emisor rechaza users inexistentes, así que nunca hay
--                  grant huérfano. CASCADE limpia si se borra la cuenta.
--   used_at     -> marca consumo single-use; NULL si aún no se canjeó

CREATE TABLE admin_grants (
    token_hash TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
);

CREATE INDEX idx_admin_grants_expires ON admin_grants(expires_at);
