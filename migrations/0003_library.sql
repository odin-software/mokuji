-- 0003_library.sql
--
-- Biblioteca de manga por usuario, con manga normalizado (compartido entre
-- usuarios) y entries personales que apuntan a cada manga.
--
-- Decisiones:
--   manga.source + source_id UNIQUE -> un mismo manga existe una sola vez,
--     compartido entre los usuarios que lo agregan
--   library_entries.user_id + manga_id UNIQUE -> un usuario no puede
--     agregar el mismo manga dos veces a su biblioteca
--   list_visibility per-usuario (public por default), togglable en /settings
--     (commit siguiente)
--   status enum-ish en código: reading, planned, completed

ALTER TABLE users ADD COLUMN list_visibility TEXT NOT NULL DEFAULT 'public';

CREATE TABLE manga (
    id         INTEGER PRIMARY KEY,
    source     TEXT    NOT NULL,           -- 'anilist'; 'mangadex' llega en fase 2
    source_id  TEXT    NOT NULL,           -- ID en la fuente externa (string para soportar UUIDs)
    title      TEXT    NOT NULL,
    cover_url  TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,           -- última vez que refrescamos metadata
    UNIQUE (source, source_id)
);

CREATE TABLE library_entries (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    manga_id     INTEGER NOT NULL REFERENCES manga(id) ON DELETE CASCADE,
    status       TEXT    NOT NULL DEFAULT 'reading',
    last_chapter TEXT,
    is_favorite  INTEGER NOT NULL DEFAULT 0,
    source_url   TEXT,                     -- donde el usuario lee este manga
    notes        TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    UNIQUE (user_id, manga_id)
);

CREATE INDEX idx_library_user            ON library_entries(user_id);
CREATE INDEX idx_library_user_status     ON library_entries(user_id, status);
CREATE INDEX idx_library_user_updated_at ON library_entries(user_id, updated_at DESC);
CREATE INDEX idx_library_manga           ON library_entries(manga_id);
