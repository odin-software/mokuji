-- 0015_drop_last_chapter.sql
--
-- El cursor (last_read_chapter_number) pasa a ser la única fuente de
-- progreso (decisión D1 de docs/ux-auditoria.md). last_chapter era texto
-- libre de antes del release tracking: no movía badges, /releases ni el
-- digest, y en el detalle convivía con el cursor mostrando otro número.
--
-- Antes de borrarla se rescata lo que tenga:
--   - Numérico ("252", "12.5"): sube el cursor si es mayor. MAX con
--     COALESCE para no bajar un cursor que ya iba más adelante.
--   - No numérico ("vol 3"): no se puede convertir en cursor, así que se
--     antepone a las notas como "progreso anterior: …" para no perderlo.
--     En prod no hay casos, pero sí puede haberlos en instancias
--     self-hosted.
-- "Numérico" = no vacío, solo dígitos y puntos, y a lo más un punto.

UPDATE library_entries
SET last_read_chapter_number = MAX(COALESCE(last_read_chapter_number, 0), CAST(trim(last_chapter) AS REAL))
WHERE trim(last_chapter) <> ''
  AND trim(last_chapter) NOT GLOB '*[^0-9.]*'
  AND trim(last_chapter) NOT GLOB '*.*.*'
  AND CAST(trim(last_chapter) AS REAL) > 0;

UPDATE library_entries
SET notes = 'progreso anterior: ' || trim(last_chapter)
            || CASE WHEN COALESCE(notes, '') <> '' THEN char(10) || notes ELSE '' END
WHERE trim(COALESCE(last_chapter, '')) <> ''
  AND (trim(last_chapter) GLOB '*[^0-9.]*' OR trim(last_chapter) GLOB '*.*.*');

ALTER TABLE library_entries DROP COLUMN last_chapter;
