-- 0011_username.sql
--
-- Agrega username/slug al perfil público (ADR pendiente; ver
-- decisiones-pendientes.md). Hoy el perfil vive en /u/{numeric_id}; este
-- cambio permite /u/{slug} para URLs human-friendly y portables.
--
-- Columnas nuevas en users:
--   - slug                    : handle público actual (NULL hasta que el
--                               user lo elija). UNIQUE case-insensitive.
--   - slug_changed_at         : unix seconds del último cambio. Sirve para
--                               enforcement del cooldown de 30 días.
--   - previous_slug           : slug anterior, "reservado" para que nadie
--                               más lo tome durante el cooldown (evita
--                               squatting/impersonation tras un cambio).
--   - previous_slug_until     : unix seconds hasta cuándo dura la reserva.
--                               Pasada esa fecha el slug viejo se libera.
--
-- Los UNIQUE indexes son parciales (WHERE NOT NULL) para no colisionar
-- entre usuarios sin slug. COLLATE NOCASE hace que 'Alice' y 'alice'
-- choquen.
--
-- No populamos slug para users existentes — la migration deja el column
-- vacío y el banner en home invita a elegirlo. El perfil sigue accesible
-- por /u/{id} hasta que el user setee un slug; tras eso, /u/{id} hace
-- 301 a /u/{slug}.

ALTER TABLE users ADD COLUMN slug TEXT;
ALTER TABLE users ADD COLUMN slug_changed_at INTEGER;
ALTER TABLE users ADD COLUMN previous_slug TEXT;
ALTER TABLE users ADD COLUMN previous_slug_until INTEGER;

CREATE UNIQUE INDEX idx_users_slug
  ON users(slug COLLATE NOCASE)
  WHERE slug IS NOT NULL;

CREATE UNIQUE INDEX idx_users_previous_slug
  ON users(previous_slug COLLATE NOCASE)
  WHERE previous_slug IS NOT NULL;
