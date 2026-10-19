# Import de MyAnimeList — diseño

2026-10-19 · estado: aprobado (decisiones del agente aceptadas tal cual)

## Por qué

Pedido de usuarios (feedback F4 en `docs/ux-auditoria.md`) y del propio
autor: quien llega de MAL no va a cargar 100+ mangas a mano. Importar la
lista baja la barrera de entrada, que es justo lo que busca v1.x de cara a
promoción.

La condición del autor: **tener en cuenta la biblioteca actual del user**.
El import no puede duplicar, ni pisar decisiones que el user ya tomó en
mokuji, ni hacerle perder progreso.

## Objetivos

- Subir el export de manga de MAL desde `/settings` y terminar con la
  biblioteca poblada con estado y progreso.
- Respetar lo que ya existe: cero duplicados y el progreso solo avanza.
- Ver un resumen claro del resultado apenas termina.
- Que el release tracking funcione para lo importado como "leyendo", sin
  degradar el tracking de los demás usuarios.

## No-objetivos

- Listas de anime.
- Scores, tags, comentarios, fechas, volúmenes y relecturas de MAL.
- Sincronización continua con MAL u OAuth con su API.
- Importar desde AniList, Kitsu u otros.
- Pulido visual: la UI se rehace en la versión de UX con accesibilidad.
  Aquí basta un form correcto (label asociado, error anunciado).

## Anti-objetivos (fallaría aunque "funcione")

- Agregar el manga equivocado por adivinar con el título.
- Cambiar estado, notas, favorito o URL de una entry que ya existía.
- Que un import grande atrase los releases de otros usuarios.
- Guardar el archivo subido.

## Restricciones

- Sin dependencias nuevas: `encoding/xml` y `compress/gzip` de stdlib.
- Server-rendered, sin JS (ADR 0002).
- Prod corre en mimir con 1 GB de RAM: tamaños acotados.
- AniList: ~90 req/min por IP, compartido con la búsqueda.
- El worker de releases procesa `BatchSize = 50` filas por tick de 1 h y
  prioriza las filas nunca polleadas (`next_check_at IS NULL`).

## Decisiones

Todas son **del agente**, salvo la 6, que nace del pedido del autor. Están
para revisar.

### 1. Dónde vive

Una sección "importar de MyAnimeList" en `/settings`, junto a "exportar mis
datos". Tiene una línea de instrucciones con link a la página de export de
MAL (`https://myanimelist.net/panel.php?go=export`, opción "Export My Manga
List"), un input de archivo y un botón. Endpoint: `POST
/settings/import/mal`.

### 2. Qué archivo acepta

- Un archivo `.xml` o `.xml.gz`. El gzip se detecta por los magic bytes
  `1f 8b`, no por la extensión.
- Máximo 5 MB subidos y 20 MB descomprimidos (`io.LimitReader`). Si se
  pasa, se rechaza con un mensaje claro, no se trunca en silencio.
- Si es un export de **anime** (no hay elementos `<manga>`), se rechaza
  con "ese es tu export de anime; necesitamos el de manga".
- Se ignora todo menos estos campos por entrada: `manga_mangadb_id`,
  `manga_title`, `my_read_chapters` y `my_status`.
- **A verificar:** los nombres de tags salen de conocimiento general, no
  de documentación. Antes de implementar se confirman contra un export
  real del autor, que, anonimizado, queda como fixture de tests en
  `testdata/`.

### 3. Identidad: MAL id → AniList, sin adivinar

- AniList expone `idMal`. Se consulta en lotes de 50:
  `Page(perPage: 50) { media(idMal_in: [...], type: MANGA) { id idMal title { romaji english native } coverImage { large } } }`.
  Probado: los ids sin mapeo simplemente no vuelven.
- Título y portada con la misma regla que la búsqueda (`preferTitle`).
- **Sin fallback por título.** Lo que no mapea queda en el resumen como "no
  encontrado en el catálogo", con su título de MAL, para agregarlo a mano.
  Es el anti-objetivo 1.

### 4. Estados

| MAL | mokuji |
|---|---|
| Reading | leyendo |
| Plan to Read | por leer |
| Completed | completado |
| On-Hold | por leer (no se lee ahora y no debe generar avisos) |
| Dropped | **no se importa**, se cuenta en el resumen |
| otro / vacío | por leer |

### 5. Progreso

`my_read_chapters > 0` → cursor (`last_read_chapter_number`) con ese
valor. `0` → cursor NULL. No se usa `last_chapter`: la D1 lo elimina en
la versión de UX.

### 6. La biblioteca actual (pedido del autor)

Si el manga (mismo id de AniList) ya está en la biblioteca:

- **Solo se toca el cursor:** `MAX(cursor actual, my_read_chapters)`. El
  progreso solo avanza, la misma regla que "marcar leído".
- Estado, notas, favorito y URL **no se tocan**, aunque MAL diga otra cosa.
  Lo que el user decidió en mokuji gana.
- Si en MAL está Dropped y en mokuji existe, no se toca.
- En el resumen cuenta como "actualizado" si el cursor subió, y si no como
  "ya estaba".
- Reimportar el mismo archivo no cambia nada (idempotente).

Las escrituras a la biblioteca van en **una transacción**: o se importa
todo o nada.

### 7. Ejecución: síncrono lo barato, carril propio para el matching

- **En el request:** parsear, consultar AniList (500 entradas son 10
  llamadas) y escribir manga y entries en la transacción. Son segundos.
- **En background, después del commit:** una goroutine por import (con el
  `BackgroundCtx` del handler) recorre las entries nuevas en "leyendo" **de
  a una**: corre los matchers (`tryMatchSources`) y hace el primer poll
  **en línea**, no fire-and-forget, así no lanza cientos de goroutines.
  Usa los mismos clientes, con sus rate limiters.
- Así las filas nuevas salen de la cola con `next_check_at` ya seteado y
  **no** se cuelan al frente del worker horario (anti-objetivo 3).
- Si el server se reinicia a mitad, lo pendiente lo recoge el `Backfiller`
  al arrancar, que ya busca entries en "leyendo" sin fila en
  `manga_sources`.
- Por leer y completado no se matchean, igual que hoy.

**Presupuesto de tiempo del request.** El server tiene `WriteTimeout: 30s`
y nginx corta a los 60 s por default. Para que el camino síncrono quepa:

- Tope de **1000 entradas** por archivo (20 llamadas a AniList, unos
  10 s). Si hay más, se rechaza con un mensaje que lo dice. Las listas de
  manga en MAL rara vez pasan de ese número.
- El handler extiende su write deadline a 55 s con
  `http.NewResponseController(w).SetWriteDeadline`.
- Si AniList responde 429 o falla, el import aborta **antes** de escribir
  nada ("AniList está ocupado, intenta en unos minutos"). Como las
  escrituras van después de todas las consultas, no quedan imports a
  medias.
- MAL ids repetidos en el archivo: cuenta la primera aparición. Dos MAL
  ids que mapean al mismo manga de AniList: se fusionan con la regla de la
  decisión 6 (cursor máximo, el primer estado gana).

**Riesgo conocido, no se resuelve aquí:** una entry importada como
"leyendo" con `my_read_chapters = 0` tiene todos sus capítulos como "no
leídos" y puede llenar `/releases`. Es el mismo comportamiento de hoy al
agregar un manga sin cursor.

### 8. Límite de uso

Un limiter por usuario (`internal/ratelimit`) de 3 imports por hora. Al
pasarse responde 429 con un mensaje legible.

### 9. CSRF con archivos (cambio en una primitiva de seguridad)

Hoy `csrf.Middleware` hace `r.ParseForm()` y lee el token con
`PostFormValue`. Con `multipart/form-data`, `ParseForm` no lee el body y
deja `PostForm` vacío, así que el token llega vacío y el POST falla con
403.

- **Cambio:** si el `Content-Type` es multipart, el middleware envuelve el
  body con `http.MaxBytesReader` (6 MB) y llama a
  `r.ParseMultipartForm(1 MB en memoria, el resto a disco temporal)` antes
  de leer el token. Los demás requests no cambian.
- **Qué se ataca si está mal:** sin el límite, cualquier POST multipart
  autenticado obliga al server a leer un body arbitrario antes de validar
  el token, lo que permite agotar RAM o disco. Con el límite, el costo está
  acotado.
- **Descartado:** poner el token en el query string del `action`, porque
  queda en logs e historial.

### 10. Resultado

La respuesta del POST renderiza directamente la página de resultado, sin
redirect. No hace falta PRG: reimportar es idempotente y el resumen no
cabe en un flash (cookie de 4 KB con títulos).

Contenido:

- importados: N (x leyendo · y por leer · z completados)
- actualizados: N · ya estaban: N
- abandonados, no importados: N
- no encontrados en el catálogo: N, con la lista de títulos de MAL
- una línea: "los capítulos de lo que estás leyendo van a ir apareciendo
  en los próximos minutos"
- link a la biblioteca

Los errores (archivo inválido, export de anime, tamaño, límite de uso)
vuelven a `/settings` con el mensaje en un `role="alert"`.

### 11. Privacidad

El archivo no se guarda: se parsea y se descarta (también el temporal
del multipart). El username de MAL que trae el export se ignora. En logs
solo van conteos, nunca títulos.

## Lo que queda al builder

- Estructura de paquetes (sugerido: el handler vive en `internal/library`,
  que ya tiene `upsertManga`, `createEntry` y `tryMatchSources`; la
  consulta `idMal_in` como método nuevo del cliente AniList).
- Copy exacto del form y del resumen, en español dominicano con tuteo.
- Cómo se nombran y organizan los tests.

## Versión

Feature propia → **v1.1.0** (a confirmar por el autor). Branch
`feat/import-mal` desde `main`.
