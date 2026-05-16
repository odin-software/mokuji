-- 0008_cursor_by_number.sql
--
-- Cursor de lectura pasa de "puntero al chapter.id leído" a "número de
-- capítulo más alto leído". Motivación: con multi-source (Fase 3), una
-- serie puede tener varias filas chapter con el mismo número (un row
-- por scanlator en Comick, o un row por source). El renderer agrupa
-- por número en UI — pero el cursor por chapter_id rompe ese modelo:
-- marcar leído un row no marca leídos los hermanos del mismo número.
--
-- Solución: cursor REAL con el número como valor. Mark-read avanza al
-- MAX(cursor_actual, número_clickeado). Query "no leído" compara
-- CAST(chapter.number AS REAL) > cursor.
--
-- Edge case: chapter.number es TEXT y acepta valores no-numéricos
-- ("Special", "Extra"). CAST('Special' AS REAL) = 0 en SQLite. Eso
-- significa que esos caps caen en el "extremo inferior" del cursor —
-- aceptable porque son raros y el orden por números es el caso normal.

ALTER TABLE library_entries ADD COLUMN last_read_chapter_number REAL;

-- Backfill desde el cursor viejo: parseo el chapter.number a float.
-- Si el cap apuntado tenía número no-numérico (raro), el CAST devuelve 0
-- y el user pierde el cursor de ese cap específico — caso degradado
-- aceptable, el user puede re-marcar.
UPDATE library_entries
SET last_read_chapter_number = (
    SELECT CAST(chapter.number AS REAL)
    FROM chapter
    WHERE chapter.id = library_entries.last_read_chapter_id
)
WHERE last_read_chapter_id IS NOT NULL;

-- Drop de la columna vieja. La FK al chapter (ON DELETE SET NULL)
-- desaparece con el column drop.
ALTER TABLE library_entries DROP COLUMN last_read_chapter_id;
