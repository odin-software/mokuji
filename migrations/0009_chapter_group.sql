-- 0009_chapter_group.sql
--
-- Agrega chapter.group_name para distinguir el scanlator/grupo del
-- título del capítulo. Comick devuelve los dos por separado:
--   - title: "Zaza" (título del cap de la historia)
--   - group_name: ["Mangakakalot"] (scanlator que subió este row)
--
-- Antes confundíamos ambos: el render mostraba "leer en Zaza" cuando
-- debería ser "leer en Mangakakalot". El group_name vive en una columna
-- propia para que el UI agrupe por número y muestre las opciones
-- correctamente.
--
-- Para sources que solo tienen un grupo (Asura, MangaDex), el cliente
-- puede dejar este campo vacío — el renderer cae al nombre de la source.

ALTER TABLE chapter ADD COLUMN group_name TEXT;
