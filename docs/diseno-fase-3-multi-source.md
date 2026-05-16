# Fase 3 — Multi-source: scanlation aggregators y groups

Living doc. Cuando cierre, destilamos a ADR 0014 y lo archivamos.

## El problema

MangaDex como fuente única deja gaps reales:

1. **Mangas con baja cobertura en MD pero alta en otros sites.** Ejemplo: "The Knight Only Lives Once" tiene 1 capítulo en MD; Asura Scans publica decenas. La traducción vive afuera y nunca sube.
2. **Scanlations pre-oficiales.** Mangas con licencia (One Piece, Jujutsu Kaisen, etc.) tienen cadencia oficial via Shueisha/MangaPlus, pero scanlation groups sacan no-oficiales horas/días antes. Esas versiones no entran a MD por respeto al licensing.
3. **Manga ongoing en source nativo de la traducción.** Manhwa coreano/manhua chino tienen ecosistemas paralelos (Asura, Reaper, Webtoon traducciones de fans) donde el contenido vive primero o únicamente.

Síntoma percibido: "mokuji dice que mi manga no tiene caps nuevos, pero en X site sí los hay desde hace una semana".

## No objetivos

- **Aggregator universal**: no apuntamos a indexar todo internet. Apuntamos a ~3 sources que cubran 90% del catálogo realista del user.
- **DRM-free / hosting propio**: NO hosteamos los caps en mokuji. Seguimos siendo un tracker — el lector siempre es externo.
- **Sources oficiales de pago**: Crunchyroll Manga, MangaPlus subscription, etc. fuera de scope.
- **Reemplazo de MangaDex**: MD sigue siendo la fuente principal cuando tiene los datos. Los demás son complemento.

## Catálogo inicial de fuentes candidatas

| Source | Tipo | API | Coverage | Estado |
|--------|------|-----|----------|--------|
| MangaDex | Aggregator | JSON pública (estable) | Amplia, oficial-friendly | **YA integrado** |
| Asura Scans | Scanlation group propio | Solo HTML (cambia de dominio cada tanto) | Manhwa popular, manhwa de aventura | Candidato fuerte (mencionado por user) |
| Reaper Scans | Scanlation group propio | Solo HTML | Manhwa/manhua, overlap parcial con Asura | Candidato |
| Bato.to | Aggregator | Solo HTML | Amplia, incluye lo que MD rechaza | Candidato |
| Comick.app | Aggregator | JSON pública (no documentada) | Amplia, agrupa scanlation groups | Candidato fuerte |
| MangaPlus | Oficial Shueisha | JSON pública | Catálogo Shueisha | YA via MD externalURL |
| WeebCentral / WCO / Mangakakalot | Spam aggregators | HTML | Variable, baja calidad | Descartar |
| RSS feeds de scanlators | Por grupo | RSS/Atom | Alta calidad pero setup manual | Postergar |

**Por discutir.** ¿Cuáles entran a v1? Mi sospecha: Comick (API + cobertura amplia) + Asura (caso de user real) son los dos primeros. Bato.to en v2.

## Dimensiones de diseño

Voy a iterar con el user dimensión por dimensión, igual que F2. Cuando cierre cada una, lo anoto acá.

1. **Set de fuentes para v1.** ¿Cuáles soportamos primero? Costo: cada source = parser/cliente + tests + tracking de cambios de site (los que scrapeamos).
2. **Identificación cross-source de manga.** Cuando agregas "Solo Leveling" en mokuji, ¿cómo sabemos que el "Solo Leveling" de Asura es el mismo de MD? ¿Matching automático por título normalizado? ¿O el user elige por source manualmente al agregar?
3. **Modelo de datos: manga ↔ sources.** Hoy `manga` tiene `mangadex_id`. ¿Migrar a tabla `manga_sources(manga_id, source, source_id, status)` que soporte N? ¿Mantener `mangadex_id` como caso especial?
4. **Chapter dedup.** Si Asura publica cap 100 y MD también, ¿son el mismo cap? ¿Mostramos 2x con label de source? ¿Consolidamos por número y mostramos el más reciente? ¿Por idioma?
5. **Worker complexity.** Hoy un worker (1h tick) polleea MD. Ahora N sources × M mangas. Rate limiting cross-source (Comick API tiene su propio quota, Asura tiene anti-scraping).
6. **Manual entry escape valve.** Permitir al user agregar a mano un cap con `URL + número + título + fecha`, cuando ninguna source detecta. ¿En v1 o postergar?
7. **Compliance / self-hosters.** Asura/Comick están en zona gris legal. ¿Toggle por self-hoster para deshabilitar scraping sources? ¿O cada source es opcional via env var?
8. **UX del user.** ¿El user elige sources globalmente? ¿Por manga? ¿Default automático?
9. **Anti-scraping management.** Asura cambia de dominio cada tanto. Cuando el parser se rompe (HTML cambia), ¿cómo lo detectamos antes de que el user note? Health checks, dashboard, alertas.
10. **Cómo migra lo que ya está.** Los mangas ya en biblioteca con `mangadex_id` poblado — ¿se les pregunta al user si quiere agregar Asura? ¿auto-search en background?

---

## Decisiones cerradas

### 1. Modelo de datos: tabla `manga_sources`

```sql
CREATE TABLE manga_sources (
    manga_id        INTEGER NOT NULL REFERENCES manga(id) ON DELETE CASCADE,
    source          TEXT    NOT NULL,         -- 'mangadex' | 'asura' | 'comick' | ...
    source_id       TEXT    NOT NULL,         -- ID o slug en esa source
    status          TEXT    NOT NULL DEFAULT 'unmatched'
                            CHECK (status IN ('unmatched','matched','needs_review','no_results')),
    checked_at      INTEGER,
    next_check_at   INTEGER,
    PRIMARY KEY (manga_id, source)
);
CREATE INDEX idx_manga_sources_next_check ON manga_sources(next_check_at) WHERE status = 'matched';
```

Una fila por `(manga, source)`. Cada fila lleva su propio matching state + scheduling — el worker query selecciona "filas matched con next_check_at vencido" sin saber de qué source es, y delega al cliente correspondiente.

Migración 0007 crea la tabla y backfillea desde las columnas viejas (`manga.mangadex_id`, `manga.mangadex_status`, `mangadex_checked_at`, `mangadex_next_check_at`) — esas columnas se dropean en la misma migration. El refactor toca:

- `internal/library/library.go`: `listEntries` deja de leer `m.mangadex_status`; lee de `manga_sources` un summary status.
- `internal/library/entry.go`: `loadEntry` igual.
- `internal/releases/worker/worker.go`: query `dueMangas` se vuelve `dueSources`.
- `internal/releases/matcher/matcher.go`: ahora hay un matcher por source (interfaz), no uno global.
- `internal/releases/digest`: query de unread caps no cambia (chapter sigue ligado a manga_id).
- Tests de todo lo anterior.

Las columnas viejas se borran en la misma migration — no las llevamos como deprecated. Eso simplifica el código.

### 2. Identificación cross-source: matcher por source

Cada source implementa una interfaz `Matcher` simétrica a la actual de MangaDex:

```go
type Matcher interface {
    Match(ctx context.Context, titles ...string) (matcher.Result, error)
}
```

Al agregar un manga (`POST /library/add`), mokuji corre **todos** los matchers configurados **en paralelo** (errgroup). Por cada source que retorna `matched`, inserta una fila en `manga_sources(manga_id, source, source_id, status='matched')`. Las que retornan `needs_review` o `no_results` también se insertan con su status correspondiente — eso da al user feedback de cobertura por source en el detail page.

Errores de network / rate limit no abortan: la source queda sin fila y un reintento futuro (worker o user manual) la retoma.

UX: en `/library/entry/{id}` el user ve un listado de sources con su estado, y un "vincular manualmente" para casos `needs_review`/`no_results`.

**Costo:** cada source nueva = implementar `Search(title)` + `FetchChapters(sourceID)`. Para sources con scraping (Asura) eso es un parser HTML defensivo + tests con fixtures.

### 3. Chapter dedup: 1 fila por (cap, source) + agrupado al renderear

**Persistencia.** La tabla `chapter` mantiene 1 fila por `(manga, source, source_id)`. Cada source publica sus propios IDs; insertarlas dos veces (una por source) es trivial y fiel a la fuente. El UNIQUE constraint actual `(source, source_id)` sigue valiendo.

**Agrupado en UI.** Al renderear:
- `/library/entry/{id}` agrupa por `(manga_id, normalize(number), language)` y muestra un item por grupo con N botones "leer →" (uno por source, etiquetados: `mangadex`, `asura`, `comick`).
- `/releases` agrupa igual; el feed muestra "cap 1182 de One Piece — 3 sources" con el mismo patrón.
- El cursor `library_entries.last_read_chapter_id` apunta a UNA fila específica (la que se marcó). Para queries de "no leído" comparamos `published_at` contra el cursor, igual que hoy. Si Asura publica cap 100 y MD publica cap 100 el día siguiente, marcar leído cualquiera de los dos avanza el cursor al `published_at` del que se marcó; el otro queda "leído implícitamente" porque su `published_at` es ≤ cursor.published_at.

**Edge case: una source numera distinto a otra.** Manhwa coreanos a veces tienen "cap 100 (final)" en Asura pero MD lo divide en "100.1" y "100.2". El normalizado de number (lower + strip puntuación) los agruparía o no según el caso. Decisión: **no peleamos contra eso** — si las sources discrepan en numeración, aparecen como items separados (group key distinta). Aceptable; raro en la práctica.

**Pros:**
- Persistencia simple: cada source es independiente, sin necesidad de "merge logic" en el worker.
- UI refleja realidad sin engañar: si Asura tiene el cap y MD no, el item dice "solo en asura" y el user no se confunde.
- Cursor sigue siendo simple (1 chapter_id por entry).

**Contra:** la query de `unreadChapters` se complica un poco — necesita group-by para no contar el mismo cap dos veces. Es un cambio acotado al SQL del digest y de `/releases`.

### 4. Sources en v1: MangaDex (ya) + Asura Scans + Comick.app

Asura cubre el caso real reportado ("The Knight Only Lives Once" con muchos caps allá y 1 en MD). Comick aporta cobertura general agregada con API JSON estable.

**Constraint no técnico crítico:** no queremos banneos de Asura ni Comick. Convención de cliente HTTP que aplica a todos los scrapers nuevos:

- Rate limit conservador: 1 req/s por defecto, configurable bajando si la source lo pide.
- User-Agent honesto: `mokuji/<version> (+<base_url>; contacto: <admin>)` — algunos sites bannean por `User-Agent: Go-http-client/1.1` por default.
- Backoff exponencial en 429/503: ya tenemos el patrón en `internal/mangadex`.
- Cache local: el chapter se persiste; nunca re-fetchamos lo mismo dos veces en el mismo tick.
- `next_check_at` cooldown más largo en sources scraped (12h vs 6h de MD).
- Si Asura cambia de dominio (ocurre cada tantos meses), el dominio actual vive en env var `ASURA_BASE_URL` con default razonable; actualizar = redeploy.

**Postergado a v2:** Bato.to (parser HTML extra, overlap con MD), Reaper Scans (overlap con Asura), ManhuaPlus (Cloudflare delante, scraping bloqueable sin JS, sin API).

### 5. Worker shape: una goroutine + dispatch a N clientes

Estructura:

```go
type SourceClient interface {
    Name() string                                              // "mangadex" | "asura" | "comick"
    Search(ctx, title) ([]MangaResult, error)                  // para el matcher
    FetchChapters(ctx, sourceID, since) ([]Chapter, error)     // para el worker
}

type Worker struct {
    db      *sql.DB
    clients map[string]SourceClient  // keyed by SourceClient.Name()
    ...
}
```

En cada tick:
1. Query: `SELECT manga_id, source, source_id FROM manga_sources WHERE status='matched' AND (next_check_at IS NULL OR next_check_at < ?) AND EXISTS (SELECT 1 FROM library_entries WHERE manga_id = manga_sources.manga_id AND status='reading') ORDER BY next_check_at IS NULL DESC, next_check_at ASC LIMIT 50`.
2. Para cada fila: `client := w.clients[row.source]; chapters := client.FetchChapters(...); persist; scheduleNextCheck`.
3. `scheduleNextCheck` usa el cooldown configurado por cliente (MD=6h, scrapers=12h).

Rate limiting: cada `SourceClient` mantiene su propio `golang.org/x/time/rate.Limiter`. Cuotas por defecto:
- MangaDex: 2 req/s, ya teníamos.
- Comick: 1 req/s (no documentada, conservador).
- Asura: 0.5 req/s (HTML scraping defensivo).

`PollOne(ctx, mangaID, sourceID, source)` — la versión sincrónica usada en `library.add` post-matching — recibe ahora también `source` para el dispatch.

### 6. UX: lista de sources con status + acciones inline

En `/library/entry/{id}`, debajo del header del manga y antes del listado de capítulos, agregamos un fieldset `sources`:

```
sources
  · mangadex  ✓ matched              [reintentar]  [desvincular]
  · asura     ⚠ matching incierto     [vincular manualmente]
  · comick    × no encontrado         [reintentar]
```

Acciones:
- **reintentar**: POST que re-corre `Matcher.Match` para esa source. Útil cuando el catálogo de la source agregó el manga después.
- **vincular manualmente**: GET a un mini-form `?source=asura&q=título`, lista candidatos via `SourceClient.Search`, el user elige y se hace UPSERT con `status='matched'`. Cubre el caso `needs_review` y casos donde el matcher falla pero el manga sí existe.
- **desvincular**: borra la fila de `manga_sources` (no toca `chapter` — los caps existentes quedan; solo dejamos de polleear).

Esto reemplaza el current "badge mangadex" simple. Para `/library` (listing): el badge actual se mantiene en spirit pero se vuelve un summary cross-source: `matched` si **al menos una** source está matched (y muestra unread count combinado); `pendiente` si todas están unmatched; el resto compone razonablemente.

**Pros:** transparencia + control + bajo costo por cliente (un POST por acción, no flow complejo).
**Contra:** una fila por source = más HTML, más markup. Aceptable: ~3 sources, 1 línea cada una.

### 7. Self-hosters: env vars opt-in con default ON

Cada source scraper tiene su env var:
- `ASURA_ENABLED=true` (default)
- `COMICK_ENABLED=true` (default)
- `ASURA_BASE_URL=https://asuracomic.net` (default; cambiará cuando rote dominio)

MD siempre on (`MANGADEX_ENABLED` no existe — sería innecesario).

Cuando una source está disabled:
- El cliente no se registra en el Worker's `clients` map.
- Las filas existentes en `manga_sources` con esa source quedan dormant — no se polleean.
- La UI las marca "source deshabilitada" en gris en lugar de no mostrarlas (mantiene la transparencia).

Justificación: mokuji.org corre todo; self-hosters más conservadores apagan lo que no quieren. Default ON porque la usabilidad por defecto vale más que la cautela jurídica que un usuario individual tiene mejor info para decidir.

### 8. Health check: endpoint admin de "source health"

Cada `SourceClient` mantiene en memoria:
- `last_success_at time.Time`
- `last_error_at time.Time`
- `last_error string` (truncado a 200 chars)
- `consecutive_failures int`

Endpoint `GET /admin/sources-health` (Authorization: Bearer ADMIN_TOKEN, igual que `/admin/trigger-digest`) devuelve JSON con la lista. Si una source no falla pero tampoco tiene éxito en X horas (los scanlators no actualizaron) eso NO es un health problem — el endpoint distingue "no actividad" de "errores".

Alerta proactiva descartada: el tradeoff de email-spam con downtime corto no compensa. Yo (admin) chequeo el endpoint cuando un manga se queda atrás.

### 9. Migración de mangas existentes: backfill en background

Migration 0007:
1. Crea `manga_sources`.
2. Backfillea: `INSERT INTO manga_sources (manga_id, source, source_id, status, checked_at, next_check_at) SELECT id, 'mangadex', mangadex_id, mangadex_status, mangadex_checked_at, mangadex_next_check_at FROM manga WHERE mangadex_id IS NOT NULL OR mangadex_status != 'unmatched'`.
3. Dropea las columnas viejas de `manga` (`mangadex_id`, `mangadex_status`, `mangadex_checked_at`, `mangadex_next_check_at`).

Job de background "first-run-after-deploy" (en `main.go`, fire-and-forget al arranque, idempotente): para cada `manga` en biblioteca, corre el matcher contra Asura y Comick si no hay ya una fila en `manga_sources` para esa combinación. Inserta el resultado.

- Idempotente: si ya hay una fila para (manga, asura), skip.
- Rate-limited: el matcher hereda los limiters de cada cliente.
- Sin presión: corre en background, no bloquea el arranque del server.
- Si falla por rate limit, queda como 'unmatched'; el worker periódico retoma.

Esto cubre la primera impresión: el día del deploy, las bibliotecas existentes se van llenando solas.

## Plan de implementación

Branch `fase-3-multi-source` (ya creada). Commits chicos, subject corto en español, push individual está OK pero el merge a main es uno solo.

| # | Commit | Notas |
|---|--------|-------|
| 1 | abrir Fase 3: doc de diseño multi-source | hecho |
| 2 | ADR 0014 destilado del doc | escribir el doc canonical antes de codear |
| 3 | migration 0007: tabla `manga_sources` + backfill desde columnas viejas | drop de columnas viejas en la misma migration |
| 4 | refactor: `internal/library` lee de `manga_sources` (mangadex sigue siendo el único cliente todavía) | tests verdes con el nuevo schema; sin nuevas sources aún |
| 5 | abstracción `SourceClient` + adapta `internal/mangadex` a la interfaz | sin cambios funcionales; mismo flow, distinto wiring |
| 6 | refactor: worker + matcher trabajan sobre `manga_sources` y dispatch via `clients[source]` | suite verde antes de tocar nuevas sources |
| 7 | cliente `internal/comick` (JSON API, mejor empezar por la fácil) | rate limit 1 req/s, User-Agent honesto |
| 8 | cliente `internal/asura` (HTML scraping defensivo) | parser separado en `asura/parse.go` para que sea testeable con fixtures |
| 9 | endpoint `GET /admin/sources-health` + métricas in-memory por cliente | reusa el patrón del admin trigger-digest |
| 10 | UI `/library/entry/{id}`: fieldset "sources" con badges + acciones (reintentar / vincular / desvincular) | 3 endpoints chicos, ~150 LOC |
| 11 | UI `/library` listing: badge cross-source (matched si al menos una source matched) | template + query |
| 12 | backfill job al arranque: matchea Asura+Comick para mangas existentes | idempotente, fire-and-forget |
| 13 | actualizar `.env.example`, `docs/estado.md`, `docs/decisiones-pendientes.md` | menciones a ASURA_ENABLED, COMICK_ENABLED, ASURA_BASE_URL |
| 14 | bump `VERSION` a `v0.3.0` | release de feature mayor |
| 15 | merge `fase-3-multi-source` → `main` con env vars seteadas en mimir antes | CD deploya, backfill corre |

Riesgo principal: el refactor del schema (commits 3-6) es invasivo. Hay que mantener la suite de tests verde en cada commit. Si algo se rompe a mitad, podemos revertir 3-6 sin daño porque los commits 7+ todavía no existen.
