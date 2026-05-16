# ADR 0014: Fase 3 — Multi-source para release tracking

- **Estado**: Aceptada
- **Fecha**: 2026-05-16

## Contexto

Fase 2 ([ADR 0013](0013-fase-2-release-tracking.md)) cerró el loop de release tracking con **una sola fuente: MangaDex**. La elección fue buena para arrancar (API estable, documentada, sin scraping, terms-of-use friendly) pero al usarse con catálogo real aparecieron gaps:

1. **Mangas con baja cobertura en MD pero activos en otros sites.** Ejemplo concreto: "The Knight Only Lives Once" tiene 1 capítulo en MD; Asura Scans publica decenas. La traducción vive afuera y nunca sube.
2. **Scanlations pre-oficiales.** Mangas con licencia (One Piece, Jujutsu Kaisen, etc.) tienen cadencia oficial vía Shueisha/MangaPlus, pero scanlation groups sacan no-oficiales horas/días antes. Esas versiones no entran a MD por respeto al licensing.
3. **Manga ongoing en source nativo de la traducción.** Manhwa coreano y manhua chino tienen ecosistemas paralelos (Asura, Reaper, Webtoon traducciones de fans) donde el contenido vive primero o únicamente.

Síntoma percibido por el user: "mokuji dice que mi manga no tiene caps nuevos, pero en X site sí los hay desde hace una semana".

Diseño completo destilado de [docs/diseno-fase-3-multi-source.md](../diseno-fase-3-multi-source.md).

## Decisión

Fase 3 cubre 9 dimensiones de decisión. Cada subsección abajo es una decisión cerrada con su razón concisa.

### 1. Modelo de datos: tabla normalizada `manga_sources`

```sql
CREATE TABLE manga_sources (
    manga_id        INTEGER NOT NULL REFERENCES manga(id) ON DELETE CASCADE,
    source          TEXT    NOT NULL,           -- 'mangadex' | 'asura' | 'comick' | ...
    source_id       TEXT    NOT NULL,           -- ID o slug en esa source
    status          TEXT    NOT NULL DEFAULT 'unmatched'
                            CHECK (status IN ('unmatched','matched','needs_review','no_results')),
    checked_at      INTEGER,
    next_check_at   INTEGER,
    PRIMARY KEY (manga_id, source)
);
CREATE INDEX idx_manga_sources_next_check ON manga_sources(next_check_at) WHERE status = 'matched';
```

Una fila por `(manga, source)`. Las columnas `mangadex_id`, `mangadex_status`, `mangadex_checked_at`, `mangadex_next_check_at` de `manga` se eliminan en la misma migration (0007); se backfillean a filas de `manga_sources(source='mangadex')` antes del drop.

**Razón.** Normalizar refleja la realidad: un manga es uno, las fuentes son N. Agregar una source nueva = nuevo valor en la columna, sin migration. El worker query se vuelve data-driven (`SELECT ... FROM manga_sources WHERE next_check_at < ?`) sin importar source. Alternativas descartadas: columnas paralelas (rígido) y JSON blob (pierde índices, complica queries).

### 2. Matching cross-source: matcher por source, en paralelo al agregar

Cada source implementa la interfaz `Matcher` ya existente. Al `POST /library/add`, mokuji corre **todos** los matchers configurados en paralelo (`errgroup`). Por cada source con `Status=matched`, inserta una fila en `manga_sources`. Las que retornan `needs_review` o `no_results` también se insertan con su status — eso da feedback de coverage por source en el detail page.

**Razón.** Ya tenemos el patrón funcionando en F2 (matcher con `Status` + exact-match normalizado). Sumar sources es repetir el patrón, no inventarlo. ~3 requests simultáneos al agregar (~1-2s total) es costo aceptable de una vez por manga.

### 3. Chapter dedup: persistencia 1-fila-por-source, agrupado al renderear

La tabla `chapter` mantiene 1 fila por `(manga, source, source_id)`. UNIQUE constraint `(source, source_id)` sigue valiendo. **No deduplicamos al persistir.**

Al renderear:
- `/library/entry/{id}` y `/releases` agrupan por `(manga_id, normalize(number), language)` y muestran un item por grupo con N botones "leer →" (uno por source).
- El cursor `library_entries.last_read_chapter_id` apunta a UNA fila específica (la que se marcó). Marcar leído cualquier source avanza el cursor a su `published_at`; los demás caps del grupo quedan implícitamente leídos por `published_at <= cursor.published_at`.

Edge case aceptado: cuando las sources discrepan en numeración (cap 100 en Asura vs 100.1+100.2 en MD), aparecen como items separados. No peleamos contra eso — fuzzy-match de chapter numbers abre un saco demasiado grande.

**Razón.** Persistencia separada es la verdad fáctica. El agrupado en UI permite cambiar criterio de display sin tocar datos. Cursor sigue siendo FK simple.

### 4. Sources en v1: MangaDex (ya) + Asura Scans + Comick.app

- **MangaDex** — ya está; sigue como source principal con cobertura amplia.
- **Asura Scans** — scanlation group propio, manhwa popular, HTML scraping. Cubre el caso real reportado (The Knight Only Lives Once).
- **Comick.app** — aggregator con API JSON estable (no documentada pero usada por terceros), cobertura amplia.

Postergado a v2: Bato.to (overlap con MD, parser HTML extra), Reaper Scans (overlap con Asura), ManhuaPlus (Cloudflare delante, scraping bloqueable sin JS, sin API).

**Constraint operacional crítico:** no queremos banneos de Asura ni Comick. Convención para los clientes nuevos:
- Rate limit conservador (Asura 0.5 req/s, Comick 1 req/s, MD 2 req/s).
- User-Agent honesto: `mokuji/<version> (+<base_url>)`.
- Backoff exponencial en 429/503.
- `next_check_at` más largo en scrapers (12h vs 6h de MD).
- Dominio actual de Asura via env var `ASURA_BASE_URL` (rota cada tantos meses).

### 5. Worker shape: una goroutine, N clientes con dispatch

```go
type SourceClient interface {
    Name() string
    Search(ctx, title) ([]MangaResult, error)
    FetchChapters(ctx, sourceID string, since time.Time) ([]Chapter, error)
}

type Worker struct {
    db      *sql.DB
    clients map[string]SourceClient
}
```

Cada tick: query devuelve filas de `manga_sources` due, ordenadas por `next_check_at`. Para cada fila, dispatcha `client := w.clients[row.source]` y llama `client.FetchChapters(row.source_id, since)`. Rate limiter por cliente (cada `SourceClient` mantiene su propio `golang.org/x/time/rate.Limiter`).

`PollOne` para sync poll desde `library.add` ahora también toma `source` como argumento.

**Razón.** Un worker por source replicaría wiring (tick, query, persist). Worker pool con channels es overkill para esta escala. Lookup table `clients[row.source]` es la abstracción correcta — el worker no sabe si la source es JSON o HTML.

### 6. UX: fieldset "sources" con badges + acciones inline

En `/library/entry/{id}`, debajo del header y antes del listado de caps:

```
sources
  · mangadex  ✓ matched              [reintentar]  [desvincular]
  · asura     ⚠ matching incierto     [vincular manualmente]
  · comick    × no encontrado         [reintentar]
```

Acciones POST:
- **reintentar**: re-corre `Matcher.Match` para esa source.
- **vincular manualmente**: mini-form con `SourceClient.Search` results; el user elige.
- **desvincular**: borra la fila de `manga_sources` (los caps existentes en `chapter` se mantienen).

El badge actual de `/library` listing se vuelve un summary cross-source: `matched` si **al menos una** source está matched (con unread count combinado).

**Razón.** Transparencia: si Asura cambió de dominio y nuestro parser se rompió, el user ve "Asura: pendiente desde hace 3 días" en vez de creerle a un silencio que miente.

### 7. Self-hosters: env vars opt-in con default ON

- `ASURA_ENABLED=true` (default)
- `COMICK_ENABLED=true` (default)
- `ASURA_BASE_URL=https://asuracomic.net` (default)

Cuando una source está disabled, su cliente no se registra en el Worker; las filas existentes en `manga_sources` con esa source quedan dormant y la UI las marca "deshabilitada" en gris.

MD no tiene flag (siempre on).

**Razón.** Default ON porque la usabilidad por defecto vale más que la cautela jurídica que cada self-hoster tiene mejor información para decidir. Self-hosters más conservadores apagan lo que no quieren.

### 8. Health check: endpoint admin

Cada `SourceClient` mantiene en memoria: `last_success_at`, `last_error_at`, `last_error` (truncado a 200 chars), `consecutive_failures`. Endpoint `GET /admin/sources-health` (Authorization: Bearer `$ADMIN_TOKEN`) lista el estado por source.

El endpoint distingue "no actividad" (los scanlators no actualizaron) de "errores" (el parser se rompió). Alerta proactiva via email descartada — el tradeoff de spam con downtime corto no compensa.

**Razón.** Visibilidad operacional sin sobre-ingenierizar. Yo (admin) chequeo el endpoint cuando un manga se queda atrás. Logs estructurados igual están.

### 9. Migración de mangas existentes: backfill en background

Migration 0007:
1. Crea `manga_sources`.
2. Backfillea `INSERT INTO manga_sources ... SELECT ... FROM manga WHERE mangadex_id IS NOT NULL OR mangadex_status != 'unmatched'`.
3. Dropea las columnas viejas de `manga`.

Job de background "first-run-after-deploy" en `main.go` (fire-and-forget, idempotente): para cada `manga` en biblioteca, corre el matcher contra Asura y Comick si no hay ya fila en `manga_sources` para esa combinación. Rate-limited (hereda limiters de cada cliente). No bloquea el arranque del server.

**Razón.** Primera impresión post-deploy importa. Lazy on-demand dejaría todas las sources en "pendiente" hasta que el user abra el detail; manual full pasaría la responsabilidad al user. Background automático es la opción menos sorpresiva.

## Resumen de decisiones

| Dim | Decisión | Razón |
|-----|----------|-------|
| 1 | Tabla `manga_sources` normalizada | Manga es 1, sources son N — modelo refleja realidad |
| 2 | Matcher por source en paralelo al agregar | Reusa patrón F2, costo single-shot al add |
| 3 | Persistencia separada, dedup en UI | Verdad fáctica + flexibilidad de display |
| 4 | v1: MangaDex + Asura + Comick | Asura cubre caso real, Comick agrega cobertura amplia |
| 5 | Un worker, N clientes via dispatch | Sin replicar wiring, lookup table simple |
| 6 | Lista de sources visible con acciones | No mentir por silencio cuando una source falla |
| 7 | Env vars opt-in default ON | Self-hosters deciden; mokuji.org corre todo |
| 8 | Endpoint admin de health | Visibilidad sin alertas proactivas |
| 9 | Backfill en background al deploy | Mejor primera impresión que lazy |

## Alternativas consideradas

Detalle completo en [docs/diseno-fase-3-multi-source.md](../diseno-fase-3-multi-source.md). Las que más costaron:

- **JSON blob para sources en `manga`** (Dim 1): rechazado por pérdida de índices + complejidad de queries.
- **Source preferida por manga** (Dim 3): rechazado por matar el beneficio principal de multi-source — el punto es que cualquiera llegue primero.
- **ManhuaPlus** como source v1 (Dim 4): rechazado por Cloudflare delante (scraping bloqueable sin JS) + sin API.
- **Worker por source** (Dim 5): rechazado por replicar wiring sin ganancia clara a esta escala.
- **Source preferida automática + summary** (Dim 6): rechazado por contradecir feedback del user en F2 ("no me queda claro si pudimos halar los episodios").
- **Email alert proactivo** para health (Dim 8): rechazado por spam-vs-downtime trade-off.
- **Lazy backfill on-demand** (Dim 9): rechazado por primera impresión post-deploy.

## Consecuencias

### Más fácil

- Mangas que no tenían cobertura en MD aparecen en la app con caps de otras sources.
- Self-hosters pueden ajustar set de sources según su jurisdicción.
- Visibilidad operacional via `/admin/sources-health` cuando una source se rompa.

### Más difícil

- Mantener parsers de scraping (Asura puede cambiar HTML cualquier día).
- Health monitoring se vuelve responsabilidad operacional (no es automático).
- Más superficie de testing: cada source nueva = fixture HTML + parser + tests.
- Migration 0007 es schema-breaking: rollback requiere otra migration.

### Sin cambio

- API pública (no hay).
- Cliente SMTP, sesiones, CSRF, rate limits del auth.
- Modelo de digest semanal (consume `chapter` por user, no cambia con multi-source).

## Plan de implementación

15 commits en branch `fase-3-multi-source`. Ver [diseno-fase-3-multi-source.md §"Plan"](../diseno-fase-3-multi-source.md). Bump `VERSION` a `v0.3.0` antes del merge (feature mayor con migration breaking).
