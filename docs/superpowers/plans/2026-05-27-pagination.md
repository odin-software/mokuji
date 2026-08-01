# Pagination (v1.0.3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Paginar `/releases` (cursor-based) y `/library/entry/{id}` chapter list (offset-based). Resuelve la friction de páginas con cientos de items (caso Dr. Stone: 232 caps en una sola página) y permite a usuarios con muchos releases acumulados navegarlos por bloques.

**Architecture:** Releases usa cursor `(published_at, chapter_id)` porque caps nuevos llegan al top mientras user navega — offset saltaría items. Entry chapters usa offset porque la lista es estable (no llegan caps nuevos al medio del rango histórico) y el user puede saltar a "página 3 de 5". El handler de releases gana una versión paginada (`UnreadReleasesPage`) que coexiste con el `UnreadReleases` simple usado por el home widget. El handler de entry pagína post-grouping en Go (la SQL devuelve todos los rows porque dedupa por `(number, language)` ya en Go).

**Tech Stack:** Go stdlib + SQLite. Sin nueva dependencia. Sin migrations.

**Branch:** `feat/pagination`. Tag al cerrar: `v1.0.3`.

---

## Decisiones cerradas (2026-05-27)

- **Releases**: cursor-based con `?before=<unix>&before_id=<id>`. Page size **50** fijo (constante `ReleasesPageSize`).
- **Entry chapters**: offset-based con `?page=N` (1-indexed). Page size **100** fijo (constante `EntryChaptersPageSize`).
- **UI**: "← anterior · siguiente →" tradicional. No numerada, no infinite scroll.
- **Filtros existentes preservados** en los links (`include_read`, `sort`).
- **Si total ≤ page_size**: no se renderiza el bloque de controles.
- **Home widget** sigue usando `UnreadReleases(ctx, userID, limit)` sin cursor — no se rompe.
- **Page size fijo en código**, no env var, no query param. YAGNI.
- **Numbering**: `v1.0.3` patch — tres archivos tocados, ~150-200 LOC, no rompe nada existente.

---

## File Structure

| Archivo | Estado | Responsabilidad |
|---------|--------|-----------------|
| `internal/releases/handler/handler.go` | modificar | Agregar `ReleasesPageSize` const + `Cursor` type + `UnreadReleasesPage(ctx, userID, limit, before, beforeID) ([]Release, *Cursor, error)`. El `list` handler parsea query params, llama a la versión paginated, expone `NextCursor *Cursor` en `pageData`. |
| `internal/releases/handler/handler_test.go` | modificar | 3 tests: cursor avanza, lista corta no devuelve cursor, segunda página no incluye items de la primera. |
| `internal/library/entry.go` | modificar | Agregar `EntryChaptersPageSize` const. `entryDetail` parsea `?page=N`, agrupa todos los caps en Go, slicea por `[offset:offset+size]`. `entryData` gana `Page int`, `TotalPages int`, `HasPrev bool`, `HasNext bool`. |
| `internal/library/entry_test.go` | modificar | 2 tests: paginación con 250 caps; out-of-bounds page. |
| `web/templates/releases.html` | modificar | Bloque de paginación al final del `<ul>` cuando `.NextCursor != nil`. |
| `web/templates/library/entry.html` | modificar | Bloque de paginación al final de la `chapter-list` cuando `TotalPages > 1`. Preserva `include_read` y `sort` en los links. |
| `VERSION` | modificar | `v1.0.2` → `v1.0.3`. |

**Convenciones obligatorias:**

- Copy en español dominicano (tuteo). **Nunca** voseo.
- Commits chicos, subject en español, lowercase.
- Pausa explícita antes del bump y antes del merge.

---

## Task 1: Branch + paginación cursor-based en releases handler

**Files:**
- Modify: `internal/releases/handler/handler.go`
- Modify: `internal/releases/handler/handler_test.go`

### Steps

- [ ] **Step 1: Crear branch desde main**

```bash
git checkout main
git pull --ff-only origin main
git checkout -b feat/pagination
git status
```
Expected: `On branch feat/pagination`, working tree limpio, HEAD en `2bff1ff` (v1.0.2).

- [ ] **Step 2: Identificar helpers de test existentes**

```bash
grep -n "^func setup\|^func insert\|^func newTestHandler\|^func authReq" internal/releases/handler/handler_test.go
```

Usar EXACTAMENTE los helpers que el archivo ya define. Si no hay un `newTestHandler` o equivalente, usar el patrón existente en otros tests del paquete. Si los tests del paquete construyen el handler diferente que el library package, adaptar.

- [ ] **Step 3: Agregar la constante y el tipo Cursor**

En `internal/releases/handler/handler.go`, DESPUÉS de la constante `MaxReleases`:

```go
// ReleasesPageSize es el tamaño de página de /releases. Cursor-based:
// la siguiente página continúa después del último (published_at, id) de
// la anterior, evitando que caps nuevos publicados durante la navegación
// salten o dupliquen items.
const ReleasesPageSize = 50

// Cursor es la posición de paginación. Zero-value = sin cursor (primera
// página). Before/BeforeID identifican el último item de la página
// anterior; la siguiente query devuelve filas estrictamente posteriores
// a ese punto en el orden (published_at DESC, id DESC).
type Cursor struct {
	Before   time.Time
	BeforeID int64
}

// IsZero reporta si el cursor es zero (sin paginación previa).
func (c Cursor) IsZero() bool {
	return c.Before.IsZero() && c.BeforeID == 0
}
```

- [ ] **Step 4: Agregar UnreadReleasesPage (con tests primero)**

Tests primero. En `internal/releases/handler/handler_test.go`, agregar:

```go
func TestUnreadReleasesPageReturnsCursorWhenMore(t *testing.T) {
	db := setupReleasesTest(t) // o el helper real del archivo
	h := newReleasesHandler(t, db) // idem
	uid := insertReleasesUser(t, db, "alice@example.com")
	mangaID := insertReleasesManga(t, db, uid)
	// Insertar 60 caps no leídos (>ReleasesPageSize).
	for i := 1; i <= 60; i++ {
		insertReleasesChapter(t, db, mangaID, fmt.Sprintf("ch-%d", i), strconv.Itoa(i),
			time.Now().Add(-time.Duration(i)*time.Hour))
	}

	releases, next, err := h.UnreadReleasesPage(context.Background(), uid, ReleasesPageSize, Cursor{})
	if err != nil {
		t.Fatalf("UnreadReleasesPage: %v", err)
	}
	if got := len(releases); got != ReleasesPageSize {
		t.Errorf("primera página: %d items, want %d", got, ReleasesPageSize)
	}
	if next == nil {
		t.Fatal("next == nil; esperaba un cursor para la siguiente página")
	}
	// El cursor debe apuntar al último item de la página actual.
	last := releases[len(releases)-1]
	if next.BeforeID != last.ChapterID {
		t.Errorf("next.BeforeID = %d, want %d (último item)", next.BeforeID, last.ChapterID)
	}
}

func TestUnreadReleasesPageNoNextWhenFewerThanPage(t *testing.T) {
	db := setupReleasesTest(t)
	h := newReleasesHandler(t, db)
	uid := insertReleasesUser(t, db, "alice@example.com")
	mangaID := insertReleasesManga(t, db, uid)
	for i := 1; i <= 5; i++ {
		insertReleasesChapter(t, db, mangaID, fmt.Sprintf("ch-%d", i), strconv.Itoa(i),
			time.Now().Add(-time.Duration(i)*time.Hour))
	}

	releases, next, err := h.UnreadReleasesPage(context.Background(), uid, ReleasesPageSize, Cursor{})
	if err != nil {
		t.Fatalf("UnreadReleasesPage: %v", err)
	}
	if len(releases) != 5 {
		t.Errorf("items: %d, want 5", len(releases))
	}
	if next != nil {
		t.Errorf("next = %+v, want nil (no hay más)", next)
	}
}

func TestUnreadReleasesPageSecondPageDoesNotRepeat(t *testing.T) {
	db := setupReleasesTest(t)
	h := newReleasesHandler(t, db)
	uid := insertReleasesUser(t, db, "alice@example.com")
	mangaID := insertReleasesManga(t, db, uid)
	for i := 1; i <= 60; i++ {
		insertReleasesChapter(t, db, mangaID, fmt.Sprintf("ch-%d", i), strconv.Itoa(i),
			time.Now().Add(-time.Duration(i)*time.Hour))
	}

	first, next, err := h.UnreadReleasesPage(context.Background(), uid, ReleasesPageSize, Cursor{})
	if err != nil || next == nil {
		t.Fatalf("primera página falló: %v, next=%v", err, next)
	}
	second, _, err := h.UnreadReleasesPage(context.Background(), uid, ReleasesPageSize, *next)
	if err != nil {
		t.Fatalf("segunda página: %v", err)
	}
	if len(second) != 10 {
		t.Errorf("segunda página: %d items, want 10", len(second))
	}
	// Sin overlap.
	seen := map[int64]bool{}
	for _, r := range first {
		seen[r.ChapterID] = true
	}
	for _, r := range second {
		if seen[r.ChapterID] {
			t.Errorf("chapter %d aparece en ambas páginas", r.ChapterID)
		}
	}
}
```

**Notas para el implementador:**
- Los nombres `setupReleasesTest`, `newReleasesHandler`, `insertReleasesUser`, etc. son PLACEHOLDERS — adaptar a los nombres reales del archivo `handler_test.go`. Buscarlos con grep antes.
- `insertReleasesChapter` debe insertar en `chapter` Y en `library_entries` con `status='reading'`. Si no hay helper, agregarlo o usar `db.Exec` directo (mirar el patrón del archivo).

Run: `go test ./internal/releases/handler/ -run "TestUnreadReleasesPage" -v`
Expected: FAIL — `UnreadReleasesPage` undefined.

- [ ] **Step 5: Implementar UnreadReleasesPage**

En `internal/releases/handler/handler.go`, después de `UnreadReleases`:

```go
// UnreadReleasesPage devuelve una página de caps no leídos con cursor.
// Si cursor.IsZero(), arranca desde el más reciente; si no, sigue después
// del item (published_at, id) indicado.
//
// Devuelve el slice de releases y un *Cursor para la siguiente página
// (nil si no hay más). Internamente fetcha limit+1 filas para detectar si
// hay más sin un COUNT extra: si vinieron más de limit, descarta la
// última y emite cursor; si vinieron <= limit, no hay más.
func (h *Handler) UnreadReleasesPage(ctx context.Context, userID int64, limit int, cursor Cursor) ([]Release, *Cursor, error) {
	args := []any{userID}
	cursorClause := ""
	if !cursor.IsZero() {
		cursorClause = `
			AND (chapter.published_at < ?
			     OR (chapter.published_at = ? AND chapter.id < ?))`
		args = append(args, cursor.Before.UTC().Unix(), cursor.Before.UTC().Unix(), cursor.BeforeID)
	}
	args = append(args, limit+1)

	query := `
		SELECT chapter.id, chapter.source_id, chapter.number, chapter.title, chapter.language,
		       chapter.published_at, chapter.external_url,
		       manga.id, manga.title, manga.cover_url
		FROM chapter
		JOIN manga          ON manga.id = chapter.manga_id
		JOIN library_entries le ON le.manga_id = manga.id
		JOIN users          ON users.id = le.user_id
		WHERE le.user_id = ?
		  AND le.status = 'reading'
		  AND chapter.language = users.preferred_language
		  AND (le.last_read_chapter_number IS NULL
		       OR CAST(chapter.number AS REAL) > le.last_read_chapter_number)
		  AND chapter.id = (
		    SELECT c2.id FROM chapter c2
		    WHERE c2.manga_id = chapter.manga_id
		      AND c2.number = chapter.number
		      AND c2.language = chapter.language
		    ORDER BY c2.published_at DESC, c2.id DESC
		    LIMIT 1
		  )` + cursorClause + `
		ORDER BY chapter.published_at DESC, chapter.id DESC
		LIMIT ?
	`

	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	out, err := scanReleases(rows)
	if err != nil {
		return nil, nil, err
	}

	if len(out) <= limit {
		return out, nil, nil
	}
	// Descartar el +1 y armar el cursor desde el último item de la página real.
	out = out[:limit]
	last := out[len(out)-1]
	return out, &Cursor{Before: last.PublishedAt, BeforeID: last.ChapterID}, nil
}

// scanReleases extrae las filas comunes de las dos queries (UnreadReleases
// y UnreadReleasesPage) a una helper. Mantiene el loop de Scan + map
// adentro en un solo lugar.
func scanReleases(rows *sql.Rows) ([]Release, error) {
	var out []Release
	for rows.Next() {
		var (
			r           Release
			sourceID    string
			number      sql.NullString
			title       sql.NullString
			language    sql.NullString
			publishedAt sql.NullInt64
			externalURL sql.NullString
			coverURL    sql.NullString
		)
		if err := rows.Scan(
			&r.ChapterID, &sourceID, &number, &title, &language,
			&publishedAt, &externalURL,
			&r.MangaID, &r.MangaTitle, &coverURL,
		); err != nil {
			return nil, err
		}
		r.ChapterNumber = number.String
		r.ChapterTitle = title.String
		r.Language = language.String
		if publishedAt.Valid {
			r.PublishedAt = time.Unix(publishedAt.Int64, 0).UTC()
		}
		if externalURL.Valid && externalURL.String != "" {
			r.ReaderURL = externalURL.String
		} else {
			r.ReaderURL = "https://mangadex.org/chapter/" + sourceID
		}
		r.MangaCoverURL = coverURL.String
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
```

**Refactor del `UnreadReleases` original**: actualizar para que use el mismo `scanReleases` helper en lugar de duplicar el bloque de Scan. Esto evita drift entre las dos queries.

Run: `go test ./internal/releases/handler/ -run "TestUnreadReleasesPage" -v`
Expected: 3 PASS.

- [ ] **Step 6: Actualizar el `list` handler para usar cursor desde query**

En `internal/releases/handler/handler.go`, modificar `pageData` y `list`:

```go
type pageData struct {
	Session    *session.Session
	Releases   []Release
	NextCursor *Cursor // nil = no hay más
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}

	cursor := parseReleasesCursor(r)
	releases, next, err := h.UnreadReleasesPage(r.Context(), s.UserID, ReleasesPageSize, cursor)
	if err != nil {
		h.logger.Error("listar releases", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "layout", pageData{
		Session:    s,
		Releases:   releases,
		NextCursor: next,
	}); err != nil {
		h.logger.Error("renderizar releases", "err", err)
	}
}

// parseReleasesCursor extrae el cursor de los query params. Tolerante:
// si los valores son inválidos o faltan, devuelve cursor zero (= primera
// página). Eso evita errores 400 por links viejos / bots con params
// raros.
func parseReleasesCursor(r *http.Request) Cursor {
	beforeRaw := r.URL.Query().Get("before")
	beforeIDRaw := r.URL.Query().Get("before_id")
	if beforeRaw == "" || beforeIDRaw == "" {
		return Cursor{}
	}
	beforeUnix, err := strconv.ParseInt(beforeRaw, 10, 64)
	if err != nil {
		return Cursor{}
	}
	beforeID, err := strconv.ParseInt(beforeIDRaw, 10, 64)
	if err != nil {
		return Cursor{}
	}
	return Cursor{
		Before:   time.Unix(beforeUnix, 0).UTC(),
		BeforeID: beforeID,
	}
}
```

- [ ] **Step 7: Build + tests**

```bash
go build ./...
go test ./...
```
Expected: PASS. Existing tests no rompen — `UnreadReleases` mantiene su firma para el home widget.

- [ ] **Step 8: Voseo grep**

```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|\bvos\b)' internal/releases/handler/handler.go internal/releases/handler/handler_test.go
```
Expected: empty en líneas nuevas.

- [ ] **Step 9: Commit**

```bash
git add internal/releases/handler/
git commit -m "$(cat <<'EOF'
releases: paginación cursor-based con UnreadReleasesPage

Cursor (published_at, chapter_id) DESC. Page size 50 fijo. Fetcha limit+1
para detectar siguiente sin COUNT extra. UnreadReleases original
preservado para el home widget. scanReleases helper compartido evita drift.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: UI de paginación en releases.html

**Files:**
- Modify: `web/templates/releases.html`

### Steps

- [ ] **Step 1: Agregar el bloque de "siguiente" después del `</ul>` de releases**

En `web/templates/releases.html`, después del `</ul>` que cierra la lista (alrededor de línea 55) y ANTES del `{{else}}` que renderiza el empty state:

```html
</ul>
{{if .NextCursor}}
<nav class="pagination" aria-label="paginación de releases">
  <a href="/releases?before={{.NextCursor.Before.Unix}}&before_id={{.NextCursor.BeforeID}}" class="pagination-link">
    siguiente →
  </a>
</nav>
{{end}}
{{else}}
```

(El `{{else}}` ya existía — no duplicarlo, solo insertamos el `{{if .NextCursor}} ... {{end}}` BETWEEN `</ul>` y `{{else}}`.)

- [ ] **Step 2: Agregar CSS para `.pagination`**

Si el `layout.html` no tiene estilos de paginación todavía (verificar con `grep -n pagination web/templates/layout.html`), agregar al style block del layout — o dentro de un nuevo `<style>` en releases.html. Más simple: dentro de releases.html, agregar `<style>` al inicio de `{{define "content"}}` (paralelo a entry.html):

```html
{{define "content"}}
<style>
.pagination {
  margin: 2rem 0 1rem;
  display: flex;
  justify-content: center;
  gap: 1rem;
}
.pagination-link {
  display: inline-block;
  padding: 0.5rem 1rem;
  border: 1.5px solid var(--text);
  color: var(--text);
  text-decoration: none;
  font-weight: 600;
  letter-spacing: 0.02em;
}
.pagination-link:hover { background: var(--text); color: var(--bg); }
.pagination-current { color: var(--text-muted); font-size: 0.875rem; align-self: center; }
</style>
<header class="lib-header">
```

(El `<header class="lib-header">` ya existía en la línea 18 — esto inserta el `<style>` antes.)

**Nota**: releases solo tiene "siguiente" — no hay "anterior" porque cursor-based no soporta navegación hacia atrás sin un stack de cursors. Mantener simple: "siguiente" navega adelante; para volver, el user usa el browser back o el link "biblioteca".

- [ ] **Step 3: Build + tests**

```bash
go build ./...
go test ./...
```
Expected: PASS.

- [ ] **Step 4: Smoke local opcional**

Si quieres verificar visualmente: en local, agregar varios manga (suficiente para tener >50 caps unread), ir a `/releases`, confirmar que el link "siguiente →" aparece, click + verifica que carga la página 2.

- [ ] **Step 5: Commit**

```bash
git add web/templates/releases.html
git commit -m "$(cat <<'EOF'
releases: link "siguiente →" en footer cuando hay más caps

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Paginación offset en entry chapter list

**Files:**
- Modify: `internal/library/entry.go`
- Modify: `internal/library/entry_test.go`

### Steps

- [ ] **Step 1: Agregar constante**

En `internal/library/entry.go`, al inicio del archivo (después de los imports), agregar:

```go
// EntryChaptersPageSize es el tamaño de página de la chapter list del
// entry detail. Offset-based: la lista es estable (no llegan caps al
// medio del rango histórico), así que el user puede saltar a "página N"
// predecible.
const EntryChaptersPageSize = 100
```

- [ ] **Step 2: Agregar tests primero**

En `internal/library/entry_test.go`, append:

```go
func TestEntryDetailPaginatesChapters(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	// Insertar 250 caps adicionales (insertMangaWithChapters ya inserta algunos).
	for i := 1; i <= 250; i++ {
		insertChapter(t, db, mangaID, fmt.Sprintf("ch-extra-%d", i), strconv.Itoa(i+10),
			"en", "https://reader.example/x", time.Now().Add(-time.Duration(i)*time.Hour))
	}

	// Page 1: debe traer EntryChaptersPageSize caps.
	r := authReq("GET", "/library/entry/"+strconv.FormatInt(entryID, 10)+"?page=1", "", uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	body := w.Body.String()
	// Heurística: contar las strings de "cap. N" en el body para validar el tamaño de página.
	// Aceptamos ±5 de tolerancia porque el body tiene otras menciones de "cap." en otros lugares.
	count := strings.Count(body, `class="chapter-num"`)
	if count != EntryChaptersPageSize {
		t.Errorf("page 1: %d caps renderizados, want %d", count, EntryChaptersPageSize)
	}
	if !strings.Contains(body, `?page=2`) {
		t.Error("page 1 debe linkear a page 2")
	}
}

func TestEntryDetailPageOutOfBoundsReturnsLastPage(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	for i := 1; i <= 10; i++ {
		insertChapter(t, db, mangaID, fmt.Sprintf("ch-%d", i), strconv.Itoa(i),
			"en", "https://reader.example/x", time.Now().Add(-time.Duration(i)*time.Hour))
	}

	// Solo hay 1 página. Pedir page=999 — esperamos que NO crashee y muestre items del rango válido.
	r := authReq("GET", "/library/entry/"+strconv.FormatInt(entryID, 10)+"?page=999", "", uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d (out-of-bounds debe devolver 200, no crashear)", w.Code)
	}
}
```

**Importante:**
- Los helpers `setupDB`, `newFullHandler`, `insertUser`, `insertMangaWithChapters`, `insertChapter`, `authReq` ya existen y se usan en otros tests del paquete. Adaptar si los nombres difieren.
- `insertMangaWithChapters` agrega algunos caps base — el test inserta 250 MÁS para asegurar overflow.

Run: `go test ./internal/library/ -run "TestEntryDetailPaginate|TestEntryDetailPageOut" -v`
Expected: FAIL (la paginación no está implementada).

- [ ] **Step 3: Extender entryData con campos de paginación**

En `internal/library/entry.go`, en el struct `entryData` (alrededor de línea 51-60):

```go
type entryData struct {
	Session       *session.Session
	Entry         Entry
	Chapters      []Chapter
	Sources       []EntrySource
	Sort          string // "asc" | "desc"
	IncludeRead   bool
	UnreadCount   int
	TotalChapters int

	// Paginación de la chapter list.
	Page       int // 1-indexed; siempre >= 1
	TotalPages int // total de páginas (1 si no hay overflow)
	HasPrev    bool
	HasNext    bool
}
```

- [ ] **Step 4: Modificar entryDetail para parsear page y slicear**

En la función `entryDetail` (alrededor de línea 62), después de cargar `chapters` con `loadChaptersForEntry`, ANTES del `h.render(...)` final, agregar:

```go
	// Parsear page del query string (1-indexed, default 1, clamp inválidos a 1).
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}

	totalAfterGroup := len(chapters)
	totalPages := (totalAfterGroup + EntryChaptersPageSize - 1) / EntryChaptersPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}

	// Slice por offset.
	start := (page - 1) * EntryChaptersPageSize
	end := start + EntryChaptersPageSize
	if end > totalAfterGroup {
		end = totalAfterGroup
	}
	pageChapters := chapters[start:end]
```

Luego pasar al render:

```go
	h.render(w, r, "library/entry.html", entryData{
		Session:       s,
		Entry:         entry,
		Chapters:      pageChapters,
		Sources:       sources,
		Sort:          sort,
		IncludeRead:   includeRead,
		UnreadCount:   entry.UnreadCount,
		TotalChapters: entry.TotalChapters,
		Page:          page,
		TotalPages:    totalPages,
		HasPrev:       page > 1,
		HasNext:       page < totalPages,
	})
```

(Reemplazar la llamada existente `h.render(w, r, "library/entry.html", entryData{...})` — adaptando para incluir los campos nuevos. El campo `Chapters` pasa a ser `pageChapters` en vez de `chapters`.)

- [ ] **Step 5: Run tests — must pass**

```bash
go test ./internal/library/ -run "TestEntryDetailPaginate|TestEntryDetailPageOut" -v
```
Expected: 2 PASS.

- [ ] **Step 6: Run all tests**

```bash
go test ./...
```
Expected: PASS. Si algún test pre-existente del entry detail asume el comportamiento sin paginación (ej. cuenta exactamente N caps en body), puede fallar — adaptar las aserciones para usar `page=1` explícito o relajar al `>=` en lugar de `==`.

- [ ] **Step 7: Voseo grep**

```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|\bvos\b)' internal/library/entry.go internal/library/entry_test.go
```
Expected: empty en líneas nuevas.

- [ ] **Step 8: Commit**

```bash
git add internal/library/entry.go internal/library/entry_test.go
git commit -m "$(cat <<'EOF'
entry: paginación offset en la chapter list (page=N, size 100)

Slicea post-grouping en Go. Clampa page out-of-bounds a la última válida.
Cubre el caso Dr. Stone (232 caps) → 3 páginas navegables.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: UI de paginación en entry.html

**Files:**
- Modify: `web/templates/library/entry.html`

### Steps

- [ ] **Step 1: Agregar el bloque de paginación al final de la lista de chapters**

En `web/templates/library/entry.html`, después del `</ul>` que cierra la `chapter-list` (alrededor de la línea con el último `{{end}}` del `range .Chapters`), agregar:

```html
{{if gt .TotalPages 1}}
<nav class="pagination" aria-label="paginación de capítulos">
  {{if .HasPrev}}
    <a href="/library/entry/{{.Entry.ID}}?page={{sub .Page 1}}{{if .IncludeRead}}&include_read=1{{end}}{{if eq .Sort "asc"}}&sort=asc{{end}}" class="pagination-link">
      ← anterior
    </a>
  {{end}}
  <span class="pagination-current">página {{.Page}} de {{.TotalPages}}</span>
  {{if .HasNext}}
    <a href="/library/entry/{{.Entry.ID}}?page={{add .Page 1}}{{if .IncludeRead}}&include_read=1{{end}}{{if eq .Sort "asc"}}&sort=asc{{end}}" class="pagination-link">
      siguiente →
    </a>
  {{end}}
</nav>
{{end}}
```

**Importante**: Los template funcs `add` y `sub` necesitan estar registrados en el FuncMap del paquete library. Verificar con:

```bash
grep -n "FuncMap" internal/library/library.go
```

Si NO existen, agregarlos en el FuncMap del library handler (alrededor de la línea 155):

```go
funcs := template.FuncMap{
    "csrfInput":       csrf.Input,
    "appVersion":      func() string { return version.Value },
    "feedbackEnabled": feedback.IsEnabled,
    "add":             func(a, b int) int { return a + b },
    "sub":             func(a, b int) int { return a - b },
    // ... otros funcs existentes
}
```

(Listar los funcs existentes con el grep antes — los míos arriba pueden ya estar, pero `add` y `sub` no.)

- [ ] **Step 2: Reusar el CSS de `.pagination`**

Las clases `.pagination`, `.pagination-link`, `.pagination-current` se definirán en releases.html (Task 2). Si entry.html no tiene su propio `<style>` con esas clases, agregarlas también al `<style>` block existente al inicio del template (donde están las clases `.cursor-form` agregadas en v1.0.2):

```css
.pagination {
  margin: 2rem 0 1rem;
  display: flex;
  justify-content: center;
  gap: 1rem;
}
.pagination-link {
  display: inline-block;
  padding: 0.5rem 1rem;
  border: 1.5px solid var(--text);
  color: var(--text);
  text-decoration: none;
  font-weight: 600;
  letter-spacing: 0.02em;
}
.pagination-link:hover { background: var(--text); color: var(--bg); }
.pagination-current { color: var(--text-muted); font-size: 0.875rem; align-self: center; }
```

(Si el CSS ya existe del Task 2, podría dudarse — pero como los templates se parsean por handler y `releases.html` no se parsea junto a `entry.html`, hay que duplicar el CSS en cada uno. Aceptable: ~15 líneas.)

- [ ] **Step 3: Build + tests**

```bash
go build ./...
go test ./...
```
Expected: PASS.

- [ ] **Step 4: Voseo grep**

```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|\bvos\b)' web/templates/library/entry.html internal/library/library.go
```
Expected: empty en líneas nuevas.

- [ ] **Step 5: Commit**

```bash
git add web/templates/library/entry.html internal/library/library.go
git commit -m "$(cat <<'EOF'
entry: UI de paginación "← anterior · página N de M · siguiente →"

Preserva los filtros include_read y sort en los links.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Bump VERSION + smoke + PAUSA

**Files:**
- Modify: `VERSION`
- Modify: `docs/estado.md` (cerrar v1.0.3 en el backlog)

### Steps

- [ ] **Step 1: Bump VERSION**

Replace content of `VERSION`:
```
v1.0.3
```

- [ ] **Step 2: Actualizar docs/estado.md**

En `docs/estado.md`, después de la sub-sección "### v1.0.2 — cerrado ✅", agregar:

```markdown
### v1.0.3 — cerrado ✅ (branch `feat/pagination`)

Paginación en `/releases` (cursor-based) y `/library/entry/{id}` chapter list (offset-based). Resuelve la friction del caso Dr. Stone (232 caps en una sola página) y permite navegar releases acumulados por bloques.

- **Releases**: `UnreadReleasesPage(ctx, userID, limit, cursor) ([]Release, *Cursor, error)` con cursor `(published_at, chapter_id) DESC`. Page size 50 fijo. Fetcha `limit+1` para detectar siguiente sin COUNT extra. El home widget sigue usando `UnreadReleases` sin cursor. `scanReleases` helper compartido evita drift entre queries.
- **Entry chapters**: `?page=N` offset-based. Page size 100. Slicea post-grouping en Go (no en SQL, porque la dedup por `(number, language)` sucede en Go). Clampa `page > totalPages` al último válido.
- **UI**: "← anterior · página N de M · siguiente →" en entry; "siguiente →" único en releases (cursor no soporta back nativamente). Preserva `include_read` y `sort` en los links.
- **CSS** compartido `.pagination` + `.pagination-link` + `.pagination-current` (15 LOC, duplicado en releases.html y entry.html por la separación de template sets).
```

En el "### Backlog post-v1.0.2" → renombrar a "### Backlog post-v1.0.3" (y remover el item de paginación si lo tenía — sino no aplica).

- [ ] **Step 3: Smoke build**

```bash
make build
strings ./bin/mokuji | grep -E "^v1\.0\.3$" | head -3
```
Expected: `v1.0.3` inyectada.

- [ ] **Step 4: Suite completa**

```bash
go test ./...
```
Expected: PASS.

- [ ] **Step 5: Voseo grep final**

```bash
grep -rnE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|\bvos\b)' internal/releases/handler/handler.go internal/library/entry.go web/templates/releases.html web/templates/library/entry.html 2>/dev/null
```
Expected: empty en líneas nuevas (matches pre-existentes en otras líneas no son del scope).

- [ ] **Step 6: Commit**

```bash
git add VERSION docs/estado.md
git commit -m "$(cat <<'EOF'
v1.0.3

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 7: PAUSA OBLIGATORIA**

**Detenerse acá.** No taggear, no pushear, no mergear.

El user revisa la branch `feat/pagination` y decide:

1. Probar en local con un manga grande (Dr. Stone u otro) — confirmar que la chapter list ahora se pagina en bloques de 100, y que `?page=2` muestra los siguientes 100.
2. Probar `/releases` con muchos caps unread acumulados — confirmar link "siguiente →".
3. Aprobar el merge.

Pasos manuales del user post-aprobación:

```bash
git tag -a v1.0.3 -m "v1.0.3 — paginación en releases y entry chapters"
git checkout main && git merge --ff-only feat/pagination
git push origin main          # dispara CD
git push origin v1.0.3
git branch -d feat/pagination
```

---

## Self-Review

**Spec coverage:**

| Decisión | Task que la cubre |
|----------|--------------------|
| Releases cursor-based con `?before=<unix>&before_id=<id>` | Task 1 (handler + tests) |
| Releases page size 50 fijo | Task 1 (constante `ReleasesPageSize`) |
| Entry chapters offset-based con `?page=N` | Task 3 |
| Entry chapters page size 100 fijo | Task 3 (constante `EntryChaptersPageSize`) |
| UI "← anterior · siguiente →" en entry | Task 4 |
| UI "siguiente →" en releases | Task 2 |
| Preservar filtros existentes | Task 4 (en el `<a href>` se incluye `include_read` y `sort` cuando aplica) |
| No mostrar controles si total ≤ page_size | Task 2 (`{{if .NextCursor}}`) + Task 4 (`{{if gt .TotalPages 1}}`) |
| Home widget no se rompe | Task 1 (preserva `UnreadReleases` original con su firma) |
| Page size en código, sin env var | Task 1 + Task 3 (constantes) |

**Placeholder scan:** revisado — no quedan TBDs. Los nombres de test helpers son placeholders genéricos (`setupReleasesTest`, etc.) explicados como tales en la nota — el implementador debe adaptarlos a los nombres reales del paquete via `grep` antes de escribir.

**Type consistency:**
- `Cursor` struct definido en Task 1, usado en Task 2 (`.NextCursor.Before.Unix`, `.NextCursor.BeforeID`).
- `entryData` campos (`Page`, `TotalPages`, `HasPrev`, `HasNext`) definidos en Task 3, usados en Task 4.
- `ReleasesPageSize` y `EntryChaptersPageSize` referidos correctamente en sus respectivos archivos.

**Riesgos:**

| Riesgo | Mitigación |
|--------|-----------|
| `add`/`sub` template funcs no están en library FuncMap | Task 4 Step 1 nota verifica con grep y agrega si faltan. |
| Tests pre-existentes de entry detail asumen "todos los caps en body" | Task 3 Step 6 menciona explícitamente que pueden necesitar adaptación; el patrón fix es agregar `?page=1` explícito o relajar la aserción. |
| `scanReleases` helper extraído puede romper el `UnreadReleases` original si el orden de columnas cambia | Task 1 Step 5 refactoriza `UnreadReleases` para usar `scanReleases` en lugar de duplicar — los dos quedan en sync por construcción. |
| Cursor empty string en query params trabaja como "primera página" | `parseReleasesCursor` es tolerante (devuelve `Cursor{}` en cualquier error de parse). Defensa contra bots y links mal-pegados. |

Plan listo.
