-- Migration 0012: digest_frequency opt-in (weekly default + daily/off) + onboarded_at gate.
--
-- Reemplaza la semántica de email_digest_enabled (boolean) por digest_frequency:
--   - 'weekly' (default, comportamiento actual)
--   - 'daily'  (opt-in del onboarding o settings)
--   - 'off'    (sin emails)
--
-- email_digest_enabled queda como columna por compatibilidad (no la borramos en esta
-- migration por seguridad; el worker la ignora a partir de 0012). Limpieza opcional
-- en migration futura cuando el codebase no la referencie más.
--
-- onboarded_at es el gate del wizard de v0.7. Users existentes son backfilled
-- con created_at para que NO se vean forzados a pasar por onboarding (predatory).

ALTER TABLE users ADD COLUMN digest_frequency TEXT NOT NULL DEFAULT 'weekly';
ALTER TABLE users ADD COLUMN onboarded_at INTEGER;

-- Backfill: traducir email_digest_enabled actual a la nueva columna.
-- - Si email_digest_enabled = 1 → digest_frequency = 'weekly' (ya está por DEFAULT).
-- - Si email_digest_enabled = 0 → digest_frequency = 'off'.
UPDATE users SET digest_frequency = 'off' WHERE email_digest_enabled = 0;

-- Backfill: users existentes no pasan por onboarding (sería predatorio).
UPDATE users SET onboarded_at = created_at WHERE onboarded_at IS NULL;

-- Índice para que el query del worker filtrando por frequency sea barato cuando crezcan los users.
CREATE INDEX IF NOT EXISTS idx_users_digest_frequency ON users(digest_frequency);
