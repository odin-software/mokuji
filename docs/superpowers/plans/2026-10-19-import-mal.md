# Import de MyAnimeList — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Importar el export de manga de MAL a la biblioteca respetando lo que el user ya tiene (v1.1.0).

**Architecture:** Paquete nuevo `internal/malimport` que solo parsea. Lookup por `idMal` en el cliente AniList. El handler vive en `internal/library`: lookup → una transacción → página de resultado. Después del commit, una goroutine matchea y pollea en serie las entries nuevas en "leyendo". El CSRF middleware aprende a leer multipart con un tope de tamaño.

**Tech Stack:** Go stdlib (`encoding/xml`, `compress/gzip`, `net/http`), SQLite.

**Spec:** `docs/superpowers/specs/2026-10-19-import-mal-design.md`

## Global Constraints

- Branch `feat/import-mal`; release `v1.1.0`. Commits de una línea en español con prefijo de área y el trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. No pushear.
- Límites exactos: 5 MB subidos (6 MB en el `MaxBytesReader` del CSRF, para cubrir el overhead multipart), 20 MB descomprimidos, 1000 entradas, lotes de 50 a AniList, 3 imports por hora por usuario, write deadline de 55 s.
- Estados: Reading→`reading`, Plan to Read→`planned`, Completed→`completed`, On-Hold→`planned`, Dropped→se omite, otro→`planned`.
- Entry existente: solo `last_read_chapter_number = MAX(actual, MAL)`; nunca status, notes, is_favorite ni source_url.
- Copy en español dominicano con tuteo. Logs solo con conteos, nunca títulos.
- `go vet ./... && go test ./...` verde al cerrar cada task.

## Review Focus

1. **Un export real con tags distintos a los supuestos** → el parser devuelve `ErrInvalid` en vez de "0 importados" en silencio. Test en Task 2 (XML sin `<manga>` ni `<myinfo>`).
2. **Doble submit o reimport del mismo archivo** → no duplica y el resumen dice "ya estaban". Test en Task 4.
3. **AniList falla a mitad de lotes** → no se escribe nada. Test en Task 5.
4. **`my_read_chapters` con basura** ("", "abc", "-3") → se trata como 0 y no rompe el import. Test en Task 2.
5. **Un gzip bomb** (pocos KB que se expanden a más de 20 MB) → `ErrTooLarge`, sin leerlo entero. Test en Task 2.

---

### Task 1: CSRF acepta multipart con tope de tamaño

**Files:** Modify `internal/csrf/csrf.go`; Test `internal/csrf/csrf_test.go`

**Interfaces:**
- Produces: `const MaxMultipartBytes = 6 << 20` y `const multipartMemory = 1 << 20` en `csrf`. Si `Content-Type` empieza con `multipart/form-data`, el middleware hace `r.Body = http.MaxBytesReader(w, r.Body, MaxMultipartBytes)` y `r.ParseMultipartForm(multipartMemory)` antes de leer el token. Si el body se pasa del tope → 413 y `next` no corre. Otros content types sin cambios.

- [ ] **Step 1: Tests que fallan**
  - `TestAuthenticatedMultipartWithValidToken`: multipart con `csrf_token` válido y un archivo → `next` corre y ve el archivo en `r.MultipartForm`.
  - `TestAuthenticatedMultipartWithoutToken` → 403.
  - `TestAuthenticatedMultipartTooLarge`: archivo de `MaxMultipartBytes + 1` → 413 y `next` no corre.
- [ ] **Step 2:** `go test ./internal/csrf/` → FAIL (el primero da 403).
- [ ] **Step 3:** Implementar. Comentario en el código con el porqué (decisión 9 del spec).
- [ ] **Step 4:** `go test ./...` → PASS.
- [ ] **Step 5: Commit** — `csrf: lee el token de forms multipart con un tope de 6 MB`

---

### Task 2: Parser del export de MAL

**Files:** Create `internal/malimport/malimport.go`, `internal/malimport/malimport_test.go`, `internal/malimport/testdata/mangalist.xml`

**Interfaces:**
- Produces:
  ```go
  type Entry struct {
      MALID        int
      Title        string
      ReadChapters float64 // 0 si vacío, inválido o negativo
      Status       string  // "reading" | "planned" | "completed"; vacío = no importar (Dropped)
  }
  var ErrInvalid, ErrAnimeExport, ErrTooLarge, ErrTooManyEntries error
  const MaxDecompressed = 20 << 20
  const MaxEntries = 1000
  func Parse(r io.Reader) ([]Entry, error)
  func MapStatus(mal string) string
  ```
- Detección de gzip por los primeros 2 bytes (`bufio.Reader.Peek`). Lectura con `io.LimitReader(MaxDecompressed+1)`: si lee más de `MaxDecompressed` → `ErrTooLarge`. Tags: raíz `myanimelist`; por entrada `manga` con `manga_mangadb_id`, `manga_title`, `my_read_chapters`, `my_status`. Si hay elementos `<anime>` y ningún `<manga>` → `ErrAnimeExport`. Si no hay `<myanimelist>` o no hay ni `manga` ni `anime` → `ErrInvalid`. MAL ids repetidos: se queda la primera aparición. Más de `MaxEntries` → `ErrTooManyEntries`.
- El fixture usa el formato supuesto del spec. **Hay que validarlo contra un export real antes del release** (Task 7).

- [ ] **Step 1: Tests que fallan**
  - `TestParseFixture`: el fixture con 6 entradas (una por estado más un duplicado) devuelve 5 entries. Ejemplos: `{2,"Berserk",380,"reading"}`, Dropped con `Status == ""`.
  - `TestParseGzip`: el mismo fixture gzipeado en el test da el mismo resultado.
  - `TestMapStatus` (tabla): `Reading`, `Plan to Read`, `Completed`, `On-Hold`, `Dropped`, `""`, `"foo"`.
  - `TestParseReadChaptersGarbage` (Review Focus 4): `""`, `"abc"` y `"-3"` dan 0.
  - `TestParseAnimeExport`, `TestParseNotMAL` (Review Focus 1), `TestParseTooManyEntries` (1001 entradas).
  - `TestParseGzipBomb` (Review Focus 5): gzip de 21 MB de espacios dentro de `<myanimelist>` → `ErrTooLarge`.
- [ ] **Step 2:** `go test ./internal/malimport/` → FAIL.
- [ ] **Step 3:** Implementar con `xml.Decoder` en streaming (`Token`/`DecodeElement` por `<manga>`), no unmarshal de todo el documento.
- [ ] **Step 4:** PASS.
- [ ] **Step 5: Commit** — `malimport: parser del export de manga de MyAnimeList`

---

### Task 3: Lookup por idMal en AniList

**Files:** Modify `internal/mangasource/anilist/anilist.go`; Test `internal/mangasource/anilist/anilist_test.go`

**Interfaces:**
- Produces: `func (c *Client) ByMALIDs(ctx context.Context, ids []int) (map[int]mangasource.SearchResult, error)`. Lotes de 50 con la query `Page(perPage: 50) { media(idMal_in: $ids, type: MANGA) { id idMal title { romaji english native } coverImage { large } } }`. Título con `preferTitle(english, romaji, native)`. Los ids sin resultado no aparecen en el map. Cualquier error (red, status distinto de 200, `errors` de GraphQL) aborta y devuelve error.

- [ ] **Step 1: Tests que fallan**
  - `TestByMALIDsBatches`: 60 ids → el server fake recibe 2 requests con 50 y 10 ids; el map tiene lo que el fake devolvió.
  - `TestByMALIDsMissingOmitted`.
  - `TestByMALIDsErrorAborts`: el segundo lote responde 429 → error.
- [ ] **Step 2:** FAIL. **Step 3:** implementar. **Step 4:** PASS.
- [ ] **Step 5: Commit** — `anilist: busca mangas por id de MAL en lotes de 50`

---

### Task 4: Merge con la biblioteca (núcleo del import)

**Files:** Create `internal/library/importmal.go`, `internal/library/importmal_test.go`; Modify `internal/library/library.go` (`upsertManga` y `createEntry` aceptan un `dbtx`)

**Interfaces:**
- Consumes: `malimport.Entry` (Task 2), `map[int]mangasource.SearchResult` (Task 3).
- Produces:
  ```go
  type dbtx interface {
      ExecContext(context.Context, string, ...any) (sql.Result, error)
      QueryRowContext(context.Context, string, ...any) *sql.Row
  }
  type ImportSummary struct {
      Reading, Planned, Completed int // nuevos, por estado
      Updated, Unchanged          int // ya existían
      Dropped                     int
      NotFound                    []string // títulos de MAL, en orden del archivo
  }
  type ImportedManga struct{ MangaID int64; Title string } // nuevos en "reading", para Task 6
  func (h *Handler) applyMALImport(ctx context.Context, userID int64, entries []malimport.Entry, found map[int]mangasource.SearchResult) (ImportSummary, []ImportedManga, error)
  ```
- Todo en una sola `BeginTx`. El manga va con `source = h.src.Name()` (`"anilist"`) y `source_id = strconv.Itoa(anilistID)`. Entry nueva: `INSERT` con status y cursor (NULL si `ReadChapters == 0`). Entry existente: `UPDATE … SET last_read_chapter_number = MAX(COALESCE(last_read_chapter_number, 0), ?)` solo si `ReadChapters > 0`; `Updated` si subió y `Unchanged` si no. Dos MAL ids que mapean al mismo AniList id: el segundo se trata como existente.

- [ ] **Step 1: Tests que fallan**
  - `TestImportCreatesEntriesWithStatusAndCursor`.
  - `TestImportExistingOnlyRaisesCursor`: existente en `completed`, favorito, notes `"x"`, cursor 50, y MAL con Reading/80 → cursor 80 y lo demás intacto; `Updated == 1`.
  - `TestImportExistingNeverLowersCursor`: MAL 10 contra cursor 50 → 50; `Unchanged == 1`.
  - `TestImportDroppedSkippedAndExistingUntouched`.
  - `TestImportNotFoundListed`.
  - `TestImportIsIdempotent` (Review Focus 2): dos corridas seguidas → la segunda da todo `Unchanged`, sin filas nuevas.
  - `TestImportReturnsOnlyNewReadingForMatching`.
- [ ] **Step 2:** FAIL. **Step 3:** implementar (refactor `dbtx` sin cambiar el comportamiento de `add`). **Step 4:** `go test ./...` PASS.
- [ ] **Step 5: Commit** — `library: aplica el import de MAL respetando la biblioteca existente`

---

### Task 5: Endpoint `POST /settings/import/mal` y página de resultado

**Files:** Modify `internal/library/library.go` (`Config`, `Handler`, `Mount`, lista de pages); Create `web/templates/library/import-result.html`; Modify `internal/library/importmal.go`, `importmal_test.go`

**Interfaces:**
- Produces en `Config`: `MALResolver interface{ ByMALIDs(context.Context, []int) (map[int]mangasource.SearchResult, error) }`, `Flasher *flash.Flasher`, `ImportLimiter *ratelimit.Limiter` (default `ratelimit.New(rate.Every(20*time.Minute), 3)` si es nil). Si `MALResolver` es nil, la ruta no se monta.
- Handler `importMALHandler`: sesión → limiter por `userID` (429 con página de error simple si se pasa) → `http.NewResponseController(w).SetWriteDeadline(now+55s)` → `r.FormFile("file")` (tope de 5 MB por `header.Size`) → `malimport.Parse` → `MALResolver.ByMALIDs` → `applyMALImport` → render de `import-result.html`. Errores de usuario: flash `KindError` y redirect 303 a `/settings`, con los mensajes exactos de abajo.
- Mensajes: archivo faltante → `elige el archivo del export de MAL.`; `ErrInvalid` → `ese archivo no parece un export de MyAnimeList.`; `ErrAnimeExport` → `ese es tu export de anime; necesitamos el de manga.`; `ErrTooLarge` → `el archivo es demasiado grande (máximo 5 MB).`; `ErrTooManyEntries` → `tu lista tiene más de 1000 mangas; por ahora el import llega hasta ahí.`; error de AniList → `AniList está ocupado o no responde. intenta en unos minutos.`; limiter → `ya importaste varias veces seguidas. intenta en un rato.`
- `import-result.html` muestra los conteos de `ImportSummary`, la lista `NotFound` (si no está vacía), la línea `los capítulos de lo que estás leyendo van a ir apareciendo en los próximos minutos.` y el link a `/library`.

- [ ] **Step 1: Tests que fallan**
  - `TestImportHandlerRendersSummary` (resolver fake).
  - `TestImportHandlerAnimeExportFlashesAndRedirects`.
  - `TestImportHandlerResolverErrorWritesNothing` (Review Focus 3).
  - `TestImportHandlerRateLimited`: el cuarto POST seguido → 429.
- [ ] **Step 2:** FAIL. **Step 3:** implementar. **Step 4:** PASS.
- [ ] **Step 5: Commit** — `library: endpoint de import de MAL con resumen del resultado`

---

### Task 6: Matching y primer poll en background, en serie

**Files:** Modify `internal/library/library.go` (`tryMatchOne`), `internal/library/importmal.go`; Test `internal/library/importmal_test.go`

**Interfaces:**
- Produces: `tryMatchOne` pasa a devolver `(sourceID string, matched bool)` y deja de lanzar el poll. `tryMatchSources(ctx, mangaID, title)` conserva el comportamiento de `add` (poll fire-and-forget) usando ese retorno.
- Produces: `func (h *Handler) matchImported(ctx context.Context, mangas []ImportedManga)`: por cada manga, en orden, corre los matchers en paralelo por source y después `h.poller.PollOne` **en línea** por cada source matcheada. Para en `ctx.Done()`. El handler de Task 5 la lanza con `go h.matchImported(h.bgCtx, mangas)` después del commit, solo si `len(mangas) > 0`.

- [ ] **Step 1: Tests que fallan**
  - `TestMatchImportedPollsInlineInOrder`: el poller fake registra las llamadas; al volver `matchImported`, ya están las 2 de los 2 mangas (sin esperar goroutines).
  - `TestMatchImportedStopsOnCancel`.
  - Los tests existentes de `tryMatchSources` siguen pasando sin cambios.
- [ ] **Step 2:** FAIL. **Step 3:** implementar. **Step 4:** `go test ./...` PASS.
- [ ] **Step 5: Commit** — `library: matchea y pollea lo importado en serie sin pasar por la cola del worker`

---

### Task 7: Form en ajustes, wiring y release

**Files:** Modify `web/templates/settings.html`, `main.go`, `VERSION`, `docs/estado.md`; Test `internal/profile/profile_test.go`

- [ ] **Step 1: Test que falla:** `TestSettingsHasMALImportForm`: el HTML contiene `action="/settings/import/mal"`, `enctype="multipart/form-data"` y un `<label for=…>` del input de archivo.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3: Implementar.**
  - `settings.html`: fieldset "importar de MyAnimeList" con una línea de instrucciones y link a `https://myanimelist.net/panel.php?go=export` (rel `noopener noreferrer`), input `type="file" name="file" accept=".xml,.gz"` con label y botón `importar`. El flash de error se renderiza con `role="alert"`.
  - `main.go`: una sola instancia `al := anilist.New()` usada como `MangaSource` y `MALResolver`; pasar el `Flasher` existente.
- [ ] **Step 4:** `go vet ./... && go test ./...` → PASS.
- [ ] **Step 5: Verificación en local** (preview `mokuji`, cuenta `ux-audit@example.test`): importar el fixture con un manga que ya está en la biblioteca → el resumen cuadra, la entry existente solo cambió el cursor, en unos minutos aparecen capítulos de lo nuevo en "leyendo", y reimportar da todo "ya estaba". **Si el autor pasó su export real, importarlo también** y ajustar el parser si los tags difieren.
- [ ] **Step 6:** `VERSION` → `v1.1.0`; entrada en `docs/estado.md` con el formato de v1.0.8.
- [ ] **Step 7: Commit** — `docs: v1.1.0 — import de MyAnimeList`
- [ ] **Step 8:** Proponer el rebase/fast-forward a `main` y el tag `v1.1.0`. **No pushear sin confirmación** (el push dispara el CD).
