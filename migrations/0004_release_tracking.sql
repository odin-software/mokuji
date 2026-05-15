-- 0004_release_tracking.sql
--
-- Fase 2: tracking de capítulos publicados (release tracking).
-- Diseño completo: ADR 0013.
--
-- Cambios:
--   manga: columnas para vínculo con MangaDex + scheduling del worker
--   chapter: capítulos publicados (compartido entre users, igual que manga)
--   library_entries.last_read_chapter_id: cursor de lectura (Decisión 9 ADR 0013)
--   users: toggle de email digest, idioma preferido, soft-delete
--   deletion_log: hash one-way para audit post hard-delete
--
-- No incluido (intencional):
--   tabla unsubscribe_tokens: tokens son HMAC firmados, sin storage necesario
--   tabla chapter_reads granular: reemplazada por cursor en library_entries

-- 1. Vínculo manga ↔ MangaDex
ALTER TABLE manga ADD COLUMN mangadex_id TEXT;
ALTER TABLE manga ADD COLUMN mangadex_status TEXT NOT NULL DEFAULT 'unmatched'
    CHECK (mangadex_status IN ('unmatched','matched','needs_review','no_results'));
ALTER TABLE manga ADD COLUMN mangadex_checked_at INTEGER;
ALTER TABLE manga ADD COLUMN mangadex_next_check_at INTEGER;

-- Index parcial: solo mangas matcheados son candidatos al worker.
CREATE INDEX idx_manga_mangadex_next_check
    ON manga(mangadex_next_check_at)
    WHERE mangadex_id IS NOT NULL;

-- 2. Capítulos del manga (compartido entre users)
CREATE TABLE chapter (
    id            INTEGER PRIMARY KEY,
    manga_id      INTEGER NOT NULL REFERENCES manga(id) ON DELETE CASCADE,
    source        TEXT    NOT NULL,   -- 'mangadex' por ahora
    source_id     TEXT    NOT NULL,   -- UUID del cap en MangaDex
    number        TEXT,                -- '125', '125.5', 'Special' (TEXT por flexibilidad)
    title         TEXT,
    language      TEXT,                -- 'en', 'es', ...
    published_at  INTEGER,             -- unix epoch
    external_url  TEXT,
    created_at    INTEGER NOT NULL,
    UNIQUE (source, source_id)
);

CREATE INDEX idx_chapter_manga_published
    ON chapter(manga_id, published_at DESC);

-- 3. Cursor de lectura del usuario (Decisión 9 ADR 0013)
-- ON DELETE SET NULL: si el cap se borra por cascade del manga, el cursor
-- queda vacío en lugar de bloquear la operación.
ALTER TABLE library_entries ADD COLUMN last_read_chapter_id INTEGER
    REFERENCES chapter(id) ON DELETE SET NULL;

-- 4. Toggle de email digest (Decisión 6.3 ADR 0013).
-- Default 1 (ON): el consentimiento explícito viene del checkbox visible al
-- signup. Para usuarios existentes (creados antes de fase 2) también default
-- ON, asumiendo que la primera vez que se les muestre /releases pueden
-- destildar en /settings.
ALTER TABLE users ADD COLUMN email_digest_enabled INTEGER NOT NULL DEFAULT 1;

-- 5. Idioma preferido para los releases (Decisión 8 ADR 0013).
-- Valores soportados inicialmente: 'en', 'es'.
ALTER TABLE users ADD COLUMN preferred_language TEXT NOT NULL DEFAULT 'en';

-- 6. Soft delete con gracia de 7 días (Decisión 11 ADR 0013).
-- Mientras deleted_at IS NOT NULL y (deleted_at + 7d) > now, el usuario no
-- puede loguear normalmente; un intento muestra "cuenta pendiente de borrado".
-- Después de 7 días, cron diario hace hard delete.
ALTER TABLE users ADD COLUMN deleted_at INTEGER;
CREATE INDEX idx_users_deleted_at ON users(deleted_at) WHERE deleted_at IS NOT NULL;

-- 7. Audit log mínimo post hard-delete (Decisión 11 ADR 0013).
-- user_id_hash = SHA-256(user_id || pepper), pepper en env var.
-- No es PII recuperable; suficiente para responder a soporte tipo
-- "borré mi cuenta hace un mes, ¿la restauran?".
CREATE TABLE deletion_log (
    user_id_hash TEXT    PRIMARY KEY,
    deleted_at   INTEGER NOT NULL
);
