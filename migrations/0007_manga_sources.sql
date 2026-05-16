-- 0007_manga_sources.sql
--
-- Fase 3: una tabla manga_sources que soporta N fuentes (MangaDex + Asura
-- Scans + Comick.app + …) por manga, reemplazando las columnas mangadex_*
-- que en la práctica eran un "manga_sources de una sola source". Diseño
-- completo: ADR 0014.
--
-- La migración:
--   1. Crea manga_sources.
--   2. Backfillea las filas existentes desde manga.mangadex_*.
--   3. Dropea las columnas viejas de manga.
--
-- Drop en la misma migration porque el refactor que lee del nuevo schema
-- aterriza en el mismo commit; mantener las columnas como "deprecated"
-- complicaría el código sin beneficio real.

CREATE TABLE manga_sources (
    manga_id      INTEGER NOT NULL REFERENCES manga(id) ON DELETE CASCADE,
    source        TEXT    NOT NULL,
    source_id     TEXT    NOT NULL,
    status        TEXT    NOT NULL DEFAULT 'unmatched'
                          CHECK (status IN ('unmatched','matched','needs_review','no_results')),
    checked_at    INTEGER,
    next_check_at INTEGER,
    PRIMARY KEY (manga_id, source)
);

-- Index parcial sobre la cola del worker: solo filas matched son candidatas
-- a polleo periódico, así que indexamos exactamente esa selección.
CREATE INDEX idx_manga_sources_next_check
    ON manga_sources(next_check_at)
    WHERE status = 'matched';

-- Backfill desde columnas viejas. Solo backfilleamos cuando hay algo que
-- perder: id no nulo o status distinto del default 'unmatched'. Si el
-- status es matched, mangadex_id está poblado por convención del matcher;
-- en otros estados puede ser NULL y rellenamos con '' para satisfacer
-- NOT NULL de source_id.
INSERT INTO manga_sources (manga_id, source, source_id, status, checked_at, next_check_at)
SELECT id,
       'mangadex',
       COALESCE(mangadex_id, ''),
       mangadex_status,
       mangadex_checked_at,
       mangadex_next_check_at
FROM manga
WHERE mangadex_id IS NOT NULL OR mangadex_status != 'unmatched';

-- Dropeamos primero el índice que dependía de mangadex_id (SQLite rehúsa
-- DROP COLUMN si una columna está referenciada en un index, partial WHERE
-- incluido). ALTER TABLE DROP COLUMN funciona en SQLite ≥ 3.35 — modernc.org
-- empaqueta una versión más nueva.
DROP INDEX IF EXISTS idx_manga_mangadex_next_check;
ALTER TABLE manga DROP COLUMN mangadex_next_check_at;
ALTER TABLE manga DROP COLUMN mangadex_checked_at;
ALTER TABLE manga DROP COLUMN mangadex_status;
ALTER TABLE manga DROP COLUMN mangadex_id;
