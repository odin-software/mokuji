# Library Improvements (v1.0.2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tres mejoras a la experiencia de biblioteca + sources, bundleadas en v1.0.2: (1) permitir setear el cursor de lectura a un valor arbitrario para corregir errores y catalogar manga ya leído; (2) mostrar un badge "halando capítulos…" en sources matcheadas que todavía no tienen caps en DB; (3) `MANGADEX_ENABLED` env var con manejo gracioso de "todas las sources apagadas".

**Architecture:** Tres features independientes que comparten un branch (`feat/library-improvements`) y un tag (`v1.0.2`). Cursor: endpoint nuevo `POST /library/entry/{id}/cursor` que hace `UPDATE` directo (sin la guarda monotónica de `markChapterRead`). Badge: nueva columna calculada `Pending bool` en `EntrySource`, derivada de un `LEFT JOIN COUNT(chapter)` en `loadEntrySources`; renderizada en `entry.html`. Toggle: nueva env var `MANGADEX_ENABLED` (default `true`) gatea el wire del cliente; cuando todas las sources quedan apagadas, `main.go` salta la construcción del worker y emite un warning de startup.

**Tech Stack:** Go stdlib (`net/http`, SQLite via `modernc.org/sqlite`), templates `html/template` server-rendered. Sin nueva dependencia. Sin migrations (todos los cambios usan columnas existentes).

**Branch:** `feat/library-improvements`. Tag al cerrar: `v1.0.2`.

---

## Decisiones cerradas con el user (2026-05-26)

- **Cursor UX**: un solo input numérico "voy por el cap. ___" + botón guardar en la página de entry. Form chico, prefill con `last_read_chapter_number` actual. Cubre los 3 casos (corrección, catalogación de manga ya leído, reset al 0).
- **Cursor permite retroceso**: el handler nuevo NO usa el `MAX()` que sí usa `markChapterRead`. Eso es el fix — permite explícitamente que el user baje el cursor.
- **Badge UX**: render dentro del fieldset de sources existente, junto al status. Texto: `· halando capítulos…`. Aparece solo cuando `manga_sources.status = 'matched'` AND `COUNT(chapter WHERE manga_id = X AND source = Y) = 0`.
- **`MANGADEX_ENABLED` default**: `true` (preservar comportamiento actual). Self-hosters que no quieren MD lo setean a `false` igual que Asura/Comick.
- **All-sources-off**: app arranca normal con warning en logs. Worker no se construye. `/releases` muestra empty state existente (no cambia). Library + perfil + digest siguen funcionando como catalogación manual sin tracking automático.
- **Numbering**: `v1.0.2` (tres features chicas, no es "paquete grande").
- **Branch naming**: `feat/library-improvements` — banner pragmático que cubre las tres aunque MD toggle sea más operator-config que library-UX.

---

## File Structure

| Archivo | Estado | Responsabilidad |
|---------|--------|-----------------|
| `internal/library/entry.go` | modificar | Agregar handler `entryCursor` (POST). Registrar la ruta en `Mount`. |
| `internal/library/entry_test.go` | modificar | Tests del cursor: set arbitrario, validación, ownership. |
| `internal/library/sources.go` | modificar | Agregar campo `Pending bool` a `EntrySource`. Actualizar `loadEntrySources` query con `LEFT JOIN` para contar chapters. |
| `internal/library/sources_test.go` | modificar | Test del campo `Pending` (matched sin caps → true; matched con caps → false; needs_review → false). |
| `web/templates/library/entry.html` | modificar | (a) Agregar form de cursor sobre la lista de chapters. (b) Renderizar `· halando capítulos…` cuando `.Pending`. |
| `main.go` | modificar | Gatear `mangadex.New()` por `MANGADEX_ENABLED` (default `true`). Cuando `len(sourceClients) == 0`, log warning + skip `worker.New` + skip `library.PollOne` (pasar `nil`). |
| `.env.example` | modificar | Documentar `MANGADEX_ENABLED`. |
| `docs/self-hosting.md` | modificar | Agregar `MANGADEX_ENABLED` a la tabla "Opcionales por feature" + warning sobre el comportamiento all-off. |
| `VERSION` | modificar | `v1.0.1` → `v1.0.2`. |

**Convenciones obligatorias (heredadas):**

- Copy 100% en español dominicano (tuteo). **Nunca** voseo.
- Commits chicos, subject en español, lowercase.
- ADR sólo si hay tradeoff real. Este plan NO genera ADRs.
- Confirmar antes de push/merge a main. CD deploya al pushear.
- Pausa explícita antes del bump `v1.0.2` y antes del merge.

---

## Task 1: Branch + handler del cursor arbitrario

**Files:**
- Modify: `internal/library/entry.go` (nuevo método `entryCursor` + registro en `Mount`)
- Modify: `internal/library/entry_test.go` (3 tests)

- [ ] **Step 1: Crear branch desde main**

```bash
git checkout main
git pull --ff-only origin main
git checkout -b feat/library-improvements
git status
```
Expected: `On branch feat/library-improvements`, working tree limpio, HEAD en `7a19fb0` (v1.0.1).

- [ ] **Step 2: Test inicial — set cursor a valor arbitrario funciona y permite retroceder**

En `internal/library/entry_test.go`, encontrar el bloque de tests existentes de mark-read (usar `grep -n "TestEntryMarkRead\|TestMarkRead" internal/library/entry_test.go`). Agregar al final del archivo:

```go
func TestEntrySetCursorAllowsArbitraryValue(t *testing.T) {
	db, h, mux := setupEntryTest(t)
	userID := int64(1)
	mangaID := insertManga(t, db, "Dr. Stone")
	entryID := insertEntry(t, db, userID, mangaID, "reading")
	// Cursor empieza en 231 (caso bug del user).
	mustExec(t, db, `UPDATE library_entries SET last_read_chapter_number = 231 WHERE id = ?`, entryID)

	form := url.Values{}
	form.Set("last_read_chapter_number", "1")
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/library/entry/%d/cursor", entryID),
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withTestSession(req, userID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d, want 303. body: %s", rec.Code, rec.Body.String())
	}
	var cursor float64
	if err := db.QueryRow(`SELECT last_read_chapter_number FROM library_entries WHERE id = ?`, entryID).Scan(&cursor); err != nil {
		t.Fatalf("scan cursor: %v", err)
	}
	if cursor != 1.0 {
		t.Errorf("cursor: got %v, want 1.0 (retroceso permitido)", cursor)
	}
}

func TestEntrySetCursorRejectsNegative(t *testing.T) {
	db, h, mux := setupEntryTest(t)
	userID := int64(1)
	mangaID := insertManga(t, db, "Dr. Stone")
	entryID := insertEntry(t, db, userID, mangaID, "reading")

	form := url.Values{}
	form.Set("last_read_chapter_number", "-3")
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/library/entry/%d/cursor", entryID),
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withTestSession(req, userID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestEntrySetCursorScopedToOwner(t *testing.T) {
	db, h, mux := setupEntryTest(t)
	ownerID := int64(1)
	intruderID := int64(2)
	mangaID := insertManga(t, db, "Dr. Stone")
	entryID := insertEntry(t, db, ownerID, mangaID, "reading")
	mustExec(t, db, `UPDATE library_entries SET last_read_chapter_number = 5 WHERE id = ?`, entryID)

	form := url.Values{}
	form.Set("last_read_chapter_number", "100")
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/library/entry/%d/cursor", entryID),
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withTestSession(req, intruderID) // user que NO es el dueño
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// El handler devuelve 404 sin revelar la existencia del entry de otro user.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
	// El cursor del owner no debe cambiar.
	var cursor float64
	if err := db.QueryRow(`SELECT last_read_chapter_number FROM library_entries WHERE id = ?`, entryID).Scan(&cursor); err != nil {
		t.Fatalf("scan cursor: %v", err)
	}
	if cursor != 5.0 {
		t.Errorf("cursor del owner cambió: got %v, want 5.0", cursor)
	}
}
```

**Importante:** los helpers `setupEntryTest`, `insertManga`, `insertEntry`, `withTestSession`, `mustExec` deben existir ya en `entry_test.go` u otro archivo de tests de library. Si los nombres no matchean exactamente, leer los tests existentes (`grep -n "func setup\|func insert\|func withSession\|withTestSession" internal/library/*_test.go`) y adaptar los nombres a los reales antes de correr.

- [ ] **Step 3: Correr los 3 tests — deben fallar**

```bash
go test ./internal/library/ -run "TestEntrySetCursor" -v
```
Expected: FAIL — la ruta `/library/entry/{id}/cursor` no existe, devuelve 404 (default mux) o 405.

- [ ] **Step 4: Implementar el handler entryCursor**

En `internal/library/entry.go`, agregar el método después de `entryMarkRead` (línea ~197):

```go
// entryCursor sirve POST /library/entry/{id}/cursor. Setea el cursor de
// lectura a un valor arbitrario — explícitamente permite retroceso
// (a diferencia de markChapterRead, que es monotónico). Útil para
// corregir errores de mark-read o para catalogar manga ya leído.
func (h *Handler) entryCursor(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	entryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || entryID <= 0 {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "form inválido", http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(r.PostFormValue("last_read_chapter_number"))
	cursor, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		http.Error(w, "valor inválido para cursor", http.StatusBadRequest)
		return
	}
	if cursor < 0 {
		http.Error(w, "cursor no puede ser negativo", http.StatusBadRequest)
		return
	}

	res, err := h.db.ExecContext(r.Context(), `
		UPDATE library_entries
		SET last_read_chapter_number = ?, updated_at = ?
		WHERE id = ? AND user_id = ?
	`, cursor, time.Now().UTC().Unix(), entryID, s.UserID)
	if err != nil {
		h.logger.Error("set cursor", "err", err, "entry_id", entryID, "user_id", s.UserID)
		h.errs.InternalServerError(w, r)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Entry no existe o pertenece a otro user. 404 sin revelar.
		http.NotFound(w, r)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/library/entry/%d", entryID), http.StatusSeeOther)
}
```

- [ ] **Step 5: Registrar la ruta en Mount**

Encontrar el `Mount` del library handler (`grep -n "func.*Mount" internal/library/library.go`). Agregar la línea cerca de las otras rutas `entry/{id}/...`:

```go
mux.HandleFunc("POST /library/entry/{id}/cursor", h.entryCursor)
```

- [ ] **Step 6: Correr los 3 tests — deben pasar**

```bash
go test ./internal/library/ -run "TestEntrySetCursor" -v
```
Expected: 3 PASS.

- [ ] **Step 7: Suite completa del paquete library**

```bash
go test ./internal/library/...
```
Expected: PASS — no rompimos los tests del cursor monotónico de mark-read.

- [ ] **Step 8: Voseo grep**

```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|\bvos\b)' internal/library/entry.go internal/library/entry_test.go
```
Expected: empty (o solo matches pre-existentes en líneas que no tocaste).

- [ ] **Step 9: Commit**

```bash
git add internal/library/entry.go internal/library/entry_test.go
git commit -m "$(cat <<'EOF'
cursor: endpoint POST /library/entry/{id}/cursor para setear cursor arbitrario

Permite explícitamente retroceso, a diferencia de markChapterRead que es
monotónico. Cubre tres casos: corrección de error (cap. marcado tarde),
catalogación de manga ya leído (subir a N), reset al 0.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Form input del cursor en entry.html

**Files:**
- Modify: `web/templates/library/entry.html`

- [ ] **Step 1: Leer la estructura actual del template**

```bash
grep -n "lib-filters\|chapter-list\|TotalChapters\|last_chapter" web/templates/library/entry.html | head -15
```

El form de filtros vive en una sección `lib-filters` antes de la `chapter-list`. Vamos a meter el form del cursor en su propia sección entre el header del entry y los filtros — o, si fluye mejor, dentro del bloque de meta del header. Optar por: ANTES del `<form class="lib-filters">` y DESPUÉS del bloque `</section>` que cierra el fieldset de sources (línea ~80).

- [ ] **Step 2: Agregar el form del cursor**

Después del cierre del fieldset de sources (línea con `</section>` que sigue a `{{end}}` del `{{if .Sources}}`), y ANTES de la línea `<form method="GET" action="/library/entry/{{.Entry.ID}}" class="lib-filters">`, insertar:

```html
<form method="POST" action="/library/entry/{{.Entry.ID}}/cursor" class="cursor-form">
  {{csrfInput .Session}}
  <label for="cursor-input">vas por el capítulo</label>
  <input type="number"
         id="cursor-input"
         name="last_read_chapter_number"
         min="0"
         step="0.1"
         value="{{.Entry.LastReadChapterNumber}}"
         placeholder="0">
  <button type="submit">guardar</button>
  <p class="cursor-hint muted">
    Si marcaste un capítulo por error, pon acá el número correcto.
    También sirve si ya leíste el manga afuera y solo quieres trackear desde donde vas.
  </p>
</form>
```

**Importante:** verificar primero que el campo se llame `LastReadChapterNumber` en el struct `Entry` del template data. Buscar con:

```bash
grep -n "LastReadChapter\|last_read_chapter" internal/library/library.go internal/library/entry.go
```

Si el campo se llama `LastChapter` (text string) en lugar de `LastReadChapterNumber`, el prefill no va a funcionar. En ese caso, agregar el campo `LastReadChapterNumber` al struct `Entry` y popularlo en el query de `loadEntry`. Como TDD complementario:

1. Hacer grep arriba.
2. Si NO existe el campo, agregar `LastReadChapterNumber sql.NullFloat64` (o `float64`) al struct `Entry`.
3. Modificar el `SELECT` de `loadEntry` para incluir `le.last_read_chapter_number`.
4. Hacer pass-through al template como `{{.Entry.LastReadChapterNumber}}` o `{{.Entry.LastReadChapterNumber.Float64}}` si es NullFloat64.

- [ ] **Step 3: Agregar CSS para `.cursor-form`**

Dentro del `<style>` block existente al inicio del template (donde están los estilos de `.lib-filters`, `.chapter-list`, etc.), agregar:

```css
.cursor-form {
  display: flex;
  align-items: center;
  gap: 0.625rem;
  flex-wrap: wrap;
  padding: 0.875rem 1rem;
  background: var(--bg-2);
  border-left: 3px solid var(--accent);
  margin: 1rem 0;
}
.cursor-form label {
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--text-label);
  letter-spacing: 0.02em;
}
.cursor-form input[type="number"] {
  width: 6rem;
  padding: 0.375rem 0.5rem;
  border: 1.5px solid var(--border);
  background: var(--input-bg);
  color: var(--text);
  font: inherit;
}
.cursor-form input[type="number"]:focus { outline: none; border-color: var(--accent); }
.cursor-form button {
  padding: 0.375rem 0.875rem;
  background: var(--button-bg);
  color: var(--button-fg);
  border: none;
  font: inherit;
  font-weight: 700;
  cursor: pointer;
}
.cursor-form button:hover { background: var(--button-bg-hover); }
.cursor-form .cursor-hint {
  flex-basis: 100%;
  font-size: 0.75rem;
  margin: 0.25rem 0 0;
}
```

Si el template no tiene un `<style>` propio (algunos templates de mokuji ponen sus estilos en `layout.html` o `app.css`), poner el CSS al final del `<style>` block en layout.html o crear un bloque al inicio de entry.html.

- [ ] **Step 4: Build smoke (sin tests automatizados de templates)**

```bash
go build ./...
go test ./internal/library/...
```
Expected: clean — el cambio del template no debe romper tests existentes (los tests de library no parsean templates).

- [ ] **Step 5: Smoke manual opcional**

```bash
make compose-up && make build && ./bin/mokuji &
sleep 2
# Verificar que el form aparece (te debes loguear primero, este curl no captura el HTML completo):
curl -sS "http://localhost:8080/library/entry/1" | grep -c "cursor-form" || echo "form no aparece"
kill %1
```

- [ ] **Step 6: Voseo grep**

```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|\bvos\b)' web/templates/library/entry.html
```
Expected: empty en las líneas nuevas.

- [ ] **Step 7: Commit**

```bash
git add web/templates/library/entry.html internal/library/
git commit -m "$(cat <<'EOF'
cursor: form en entry.html para setear "voy por el capítulo X"

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

Si tuviste que tocar `Entry` struct + `loadEntry` para agregar `LastReadChapterNumber`, los archivos correspondientes (`library.go`/`entry.go`) también van en este commit.

---

## Task 3: Badge "halando capítulos…" — campo Pending en EntrySource

**Files:**
- Modify: `internal/library/sources.go` (agregar `Pending bool` + cambiar query)
- Modify: `internal/library/sources_test.go` (test del nuevo campo)

- [ ] **Step 1: Test del nuevo campo Pending**

Append to `internal/library/sources_test.go`:

```go
func TestLoadEntrySourcesMarksPendingWhenMatchedWithoutChapters(t *testing.T) {
	db, h := setupSourcesTest(t)
	mangaID := insertManga(t, db, "Dr. Stone")
	// Source matched pero SIN chapters todavía (escenario: matched recién,
	// poll inicial aún en curso o falló).
	mustExec(t, db,
		`INSERT INTO manga_sources (manga_id, source, source_id, status) VALUES (?, 'mangadex', 'md-id', 'matched')`,
		mangaID)

	sources, err := h.loadEntrySources(context.Background(), mangaID)
	if err != nil {
		t.Fatalf("loadEntrySources: %v", err)
	}
	var md *EntrySource
	for i := range sources {
		if sources[i].Source == "mangadex" {
			md = &sources[i]
			break
		}
	}
	if md == nil {
		t.Fatal("source mangadex no apareció")
	}
	if !md.Pending {
		t.Errorf("Pending: got false, want true (matched + 0 chapters)")
	}
}

func TestLoadEntrySourcesPendingFalseWhenChaptersExist(t *testing.T) {
	db, h := setupSourcesTest(t)
	mangaID := insertManga(t, db, "Dr. Stone")
	mustExec(t, db,
		`INSERT INTO manga_sources (manga_id, source, source_id, status) VALUES (?, 'mangadex', 'md-id', 'matched')`,
		mangaID)
	// Agregar 1 chapter para esa source — Pending debe ser false.
	mustExec(t, db, `
		INSERT INTO chapter (manga_id, source, source_id, number, language, published_at, created_at)
		VALUES (?, 'mangadex', 'ch-1', '1', 'en', ?, ?)
	`, mangaID, time.Now().Unix(), time.Now().Unix())

	sources, err := h.loadEntrySources(context.Background(), mangaID)
	if err != nil {
		t.Fatalf("loadEntrySources: %v", err)
	}
	for _, s := range sources {
		if s.Source == "mangadex" && s.Pending {
			t.Errorf("mangadex Pending = true; con caps existentes debe ser false")
		}
	}
}

func TestLoadEntrySourcesPendingFalseForNeedsReview(t *testing.T) {
	db, h := setupSourcesTest(t)
	mangaID := insertManga(t, db, "Dr. Stone")
	mustExec(t, db,
		`INSERT INTO manga_sources (manga_id, source, source_id, status) VALUES (?, 'mangadex', '', 'needs_review')`,
		mangaID)

	sources, err := h.loadEntrySources(context.Background(), mangaID)
	if err != nil {
		t.Fatalf("loadEntrySources: %v", err)
	}
	for _, s := range sources {
		if s.Source == "mangadex" && s.Pending {
			t.Errorf("mangadex Pending = true; needs_review nunca debe ser pending")
		}
	}
}
```

**Importante:** verificar nombres de helpers de tests (`setupSourcesTest`, `mustExec`, `insertManga`) con `grep -n "func setup\|func insert\|func mustExec" internal/library/sources_test.go internal/library/*_test.go`. Adaptar al naming real.

- [ ] **Step 2: Correr tests — deben fallar (campo Pending no existe)**

```bash
go test ./internal/library/ -run "TestLoadEntrySourcesMarksPending\|TestLoadEntrySourcesPendingFalse" -v
```
Expected: FAIL — `es.Pending undefined`.

- [ ] **Step 3: Agregar el campo Pending al struct EntrySource**

En `internal/library/sources.go`, en el struct `EntrySource` (línea ~24), agregar:

```go
	// Pending reporta cuando la source matcheó pero el chapter table
	// aún no tiene filas para ella. UI muestra "halando capítulos…"
	// para que el user no piense que el match falló.
	Pending bool
```

- [ ] **Step 4: Actualizar el query de loadEntrySources para detectar pending**

En `internal/library/sources.go`, reemplazar el SELECT existente por uno con LEFT JOIN que cuenta chapters por source:

```go
func (h *Handler) loadEntrySources(ctx context.Context, mangaID int64) ([]EntrySource, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT ms.source, ms.source_id, ms.status, ms.checked_at, ms.next_check_at,
		       COUNT(c.id) AS chapter_count
		FROM manga_sources ms
		LEFT JOIN chapter c ON c.manga_id = ms.manga_id AND c.source = ms.source
		WHERE ms.manga_id = ?
		GROUP BY ms.source, ms.source_id, ms.status, ms.checked_at, ms.next_check_at
	`, mangaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byName := map[string]EntrySource{}
	for rows.Next() {
		var (
			es           EntrySource
			checked      sql.NullInt64
			nextCheck    sql.NullInt64
			chapterCount int64
		)
		if err := rows.Scan(&es.Source, &es.SourceID, &es.Status, &checked, &nextCheck, &chapterCount); err != nil {
			return nil, err
		}
		es.HasRow = true
		_, es.Configured = h.matchers[es.Source]
		if checked.Valid {
			es.CheckedAt = time.Unix(checked.Int64, 0).UTC()
		}
		if nextCheck.Valid {
			es.NextCheckAt = time.Unix(nextCheck.Int64, 0).UTC()
		}
		// Pending = matched pero el LEFT JOIN no devolvió chapters.
		es.Pending = es.Status == matcher.StatusMatched && chapterCount == 0
		byName[es.Source] = es
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Resto idéntico — agregar placeholders para sources configuradas sin fila.
	for name := range h.matchers {
		if _, ok := byName[name]; ok {
			continue
		}
		byName[name] = EntrySource{
			Source:     name,
			Status:     matcher.StatusUnmatched,
			Configured: true,
		}
	}

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	out := make([]EntrySource, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out, nil
}
```

- [ ] **Step 5: Correr tests — deben pasar**

```bash
go test ./internal/library/ -run "TestLoadEntrySources" -v
```
Expected: PASS — los nuevos tests y los pre-existentes de `loadEntrySources`.

- [ ] **Step 6: Suite completa**

```bash
go test ./...
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/library/sources.go internal/library/sources_test.go
git commit -m "$(cat <<'EOF'
sources: agregar Pending=matched-sin-chapters en EntrySource

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Render del badge "halando capítulos…" en entry.html

**Files:**
- Modify: `web/templates/library/entry.html`

- [ ] **Step 1: Modificar el bloque de status de la source**

En `web/templates/library/entry.html`, encontrar el bloque que renderiza el status de cada source (alrededor de la línea 47-56 según el read anterior):

```html
        <span class="source-status">
          {{- if eq .Status "matched" -}}
            ✓ matched
          {{- else if eq .Status "needs_review" -}}
            ⚠ matching incierto
          {{- else if eq .Status "no_results" -}}
            × no encontrado
          {{- else -}}
            · sin intentar
          {{- end -}}
        </span>
```

Reemplazar por:

```html
        <span class="source-status">
          {{- if eq .Status "matched" -}}
            {{- if .Pending -}}
              ⏳ halando capítulos…
            {{- else -}}
              ✓ matched
            {{- end -}}
          {{- else if eq .Status "needs_review" -}}
            ⚠ matching incierto
          {{- else if eq .Status "no_results" -}}
            × no encontrado
          {{- else -}}
            · sin intentar
          {{- end -}}
        </span>
```

- [ ] **Step 2: Build + test**

```bash
go build ./...
go test ./...
```
Expected: clean.

- [ ] **Step 3: Smoke local opcional**

Levantar local, agregar un manga nuevo, ir al entry inmediatamente — debe mostrar "⏳ halando capítulos…" en las sources matcheadas hasta que el primer poll termine.

- [ ] **Step 4: Voseo grep**

```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|\bvos\b)' web/templates/library/entry.html
```
Expected: empty en líneas nuevas.

- [ ] **Step 5: Commit**

```bash
git add web/templates/library/entry.html
git commit -m "$(cat <<'EOF'
sources: badge "halando capítulos…" cuando matched + 0 caps

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: `MANGADEX_ENABLED` env var + manejo all-off

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Gate MangaDex client con env var**

En `main.go`, encontrar el bloque que construye `sourceClients` (alrededor de línea 174-176):

```go
mangadexClient := mangadex.New()
sourceClients := map[string]releasesource.Client{
    mangadexClient.Name(): mangadexClient,
}
```

Reemplazar por:

```go
sourceClients := map[string]releasesource.Client{}
if envBool("MANGADEX_ENABLED", true) {
    mangadexClient := mangadex.New()
    sourceClients[mangadexClient.Name()] = mangadexClient
    logger.Info("source mangadex habilitada")
}
```

Y mover el `sourceMatchers` (línea ~178) DESPUÉS del bloque para que solo se popule si MD está habilitada:

```go
sourceMatchers := map[string]library.Matcher{}
if mdClient, ok := sourceClients["mangadex"]; ok {
    sourceMatchers["mangadex"] = matcher.New(mdClient)
}
```

(Esto reemplaza el bloque actual de `sourceMatchers` que asume MangaDex siempre on. Verificar nombre exacto de la key con `grep -n "sourceMatchers" main.go`.)

- [ ] **Step 2: Skip worker construction si todas las sources están apagadas**

Encontrar la línea ~216 donde se construye `worker.New(worker.Config{Clients: sourceClients, ...})`. Wrap con un check:

```go
var releaseWorker *worker.Worker
if len(sourceClients) == 0 {
    logger.Warn("ninguna source de releases habilitada — el worker no arranca. La biblioteca queda como catálogo manual sin tracking automático.")
} else {
    releaseWorker, err = worker.New(worker.Config{
        Clients: sourceClients,
        // ...resto de los campos como están actualmente
    })
    if err != nil {
        logger.Error("inicializar worker de releases", "err", err)
        os.Exit(1)
    }
}
```

**Importante:** verificar el shape exacto del bloque `worker.New(...)` con `grep -B1 -A20 "worker.New" main.go` antes de editar. Los campos del Config son extensos; preservarlos todos.

- [ ] **Step 3: Si releaseWorker es nil, no arrancarlo**

Buscar dónde se llama `releaseWorker.Start(ctx)` o equivalente (probablemente en una goroutine después de la construcción):

```bash
grep -n "releaseWorker\|w\.Start\|worker\.Start" main.go
```

Wrap el start en un nil check:

```go
if releaseWorker != nil {
    go func() {
        if err := releaseWorker.Start(ctx); err != nil {
            logger.Error("worker de releases terminó con error", "err", err)
        }
    }()
}
```

- [ ] **Step 4: Pasar nil PollOne handler a library cuando no hay worker**

El `library.Handler` recibe un `poller` (probablemente del worker) que usa para `PollOne` post-match. Si no hay worker, el library handler debe recibir `nil` como poller — el código existente (`internal/library/library.go:414`) ya checka `h.poller != nil` antes de llamar.

Encontrar el wiring:

```bash
grep -n "Poller\|poller" main.go internal/library/library.go | head -10
```

Asegurar que cuando `releaseWorker == nil`, el field `Poller` del library config quede nil.

- [ ] **Step 5: Build + tests**

```bash
go build ./...
go test ./...
```
Expected: PASS.

- [ ] **Step 6: Smoke manual — all-off**

```bash
MANGADEX_ENABLED=false ASURA_ENABLED=false COMICK_ENABLED=false make build && ./bin/mokuji 2>&1 | head -20
```

Expected: el log incluye `ninguna source de releases habilitada — el worker no arranca`. El servidor arranca normal en `:8080`. `/health` responde 200.

```bash
# Verificar /releases responde (vacío):
curl -sS -o /dev/null -w "%{http_code}\n" http://localhost:8080/releases  # → 200 o 303 según auth
kill %1
```

- [ ] **Step 7: Commit**

```bash
git add main.go
git commit -m "$(cat <<'EOF'
sources: MANGADEX_ENABLED env var + arranque gracioso si todas off

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Docs — `.env.example` + `docs/self-hosting.md`

**Files:**
- Modify: `.env.example`
- Modify: `docs/self-hosting.md`

- [ ] **Step 1: Agregar MANGADEX_ENABLED a .env.example**

En `.env.example`, en la sección `# --- Sources de release tracking ---`, INSERTAR antes de las variables de Asura (porque MangaDex es la primera source mencionada en el bloque):

```
# MangaDex: source primaria, único agregador oficial sin Cloudflare ni
# challenge. Default on. Bajar a false solo si quieres una instancia que
# tracke únicamente Asura/Comick o ninguna source.
MANGADEX_ENABLED=true
MANGADEX_API_BASE_URL=
```

(Si `MANGADEX_API_BASE_URL` no se usa todavía, omitirlo. Verificar con grep antes.)

- [ ] **Step 2: Agregar fila en self-hosting.md**

En `docs/self-hosting.md`, en la tabla "Opcionales por feature", AGREGAR antes de la fila de `ASURA_ENABLED`:

```markdown
| `MANGADEX_ENABLED` | `true` | Source primaria. Apaga con `false` si prefieres solo Asura/Comick. **Si las 3 sources están apagadas**, mokuji arranca pero el worker no se construye — la biblioteca funciona como catálogo manual sin tracking automático. |
```

- [ ] **Step 3: Voseo grep**

```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|\bvos\b)' .env.example docs/self-hosting.md
```
Expected: empty en líneas nuevas.

- [ ] **Step 4: Commit**

```bash
git add .env.example docs/self-hosting.md
git commit -m "$(cat <<'EOF'
docs: documentar MANGADEX_ENABLED y comportamiento all-off

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Bump VERSION + smoke + PAUSA

**Files:**
- Modify: `VERSION`

- [ ] **Step 1: Bump VERSION**

Replace content of `VERSION` with:
```
v1.0.2
```

- [ ] **Step 2: Smoke build**

```bash
make build
strings ./bin/mokuji | grep -E "^v1\.0\.2$" | head -3
```
Expected: `v1.0.2` aparece al menos una vez.

- [ ] **Step 3: Suite completa**

```bash
go test ./...
```
Expected: PASS.

- [ ] **Step 4: Voseo grep final**

```bash
grep -rnE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|\bvos\b)' internal/library/entry.go internal/library/sources.go web/templates/library/entry.html main.go .env.example docs/self-hosting.md 2>/dev/null
```
Expected: empty en líneas nuevas.

- [ ] **Step 5: Commit**

```bash
git add VERSION
git commit -m "$(cat <<'EOF'
v1.0.2

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 6: PAUSA OBLIGATORIA**

**Detenerse aquí.** No taggear, no pushear, no mergear a `main`.

El user revisa la branch `feat/library-improvements` y decide:

1. Probar local: agregar un manga, esperar el badge "halando capítulos…", verificar que desaparece cuando el poll termina.
2. Probar el cursor: marcar un cap. tarde por error, usar el form para retroceder.
3. Probar all-off: arrancar con todas las env vars `*_ENABLED=false` y confirmar que el server arranca + warning en logs.
4. Aprobar el merge.

Pasos manuales del user post-aprobación:

- `git tag -a v1.0.2 -m "v1.0.2 — cursor arbitrario + badge poll-in-progress + MANGADEX_ENABLED"`
- `git checkout main && git merge --ff-only feat/library-improvements`
- `git push origin main` (dispara CD)
- `git push origin v1.0.2`
- `git branch -d feat/library-improvements`

---

## Self-Review

**Spec coverage:**

| Decisión | Task que la cubre |
|----------|--------------------|
| Cursor arbitrario sin guarda monotónica | Task 1 (handler + tests) + Task 2 (UI form) |
| Cubre 3 casos: corrección, catalogación, reset | Task 1 (test de retroceso) + Task 2 (placeholder y hint del form) |
| Badge "halando…" en sources matched sin caps | Task 3 (campo Pending) + Task 4 (render) |
| `MANGADEX_ENABLED` default true | Task 5 |
| All-off → app arranca con warning, worker no construido | Task 5 (skip construction + nil check en Start) |
| Docs actualizados | Task 6 |
| Numbering v1.0.2 | Task 7 |
| Branch feat/library-improvements | Task 1 |

**Placeholder scan:** revisado — no quedan TBDs. Hay un par de "verificar con grep antes de editar" donde el plan depende de naming de helpers de tests existentes; eso es trabajo legítimo del implementador y NO es un placeholder de código.

**Type consistency:**
- `EntrySource.Pending` introducido en Task 3, consumido en Task 4. Tipo `bool` consistente.
- `entryCursor` método del `library.Handler` — naming consistente con `entryMarkRead`, `entryReadAndOpen`.
- `last_read_chapter_number` columna en SQL — consistente entre `markChapterRead` (Task 1 referencia) y `entryCursor` (Task 1 implementación).

**Riesgos:**

| Riesgo | Mitigación |
|--------|-----------|
| `Entry.LastReadChapterNumber` no existe en el struct del template data | Task 2 Step 2 detecta y agrega el campo si falta. |
| El LEFT JOIN del Task 3 cambia el shape del query y rompe ordering | La cláusula `GROUP BY` preserva el resto de columnas; el orden alfabético se aplica después en código. |
| El worker es referenciado desde otros lugares en main.go (graceful shutdown, etc.) | Task 5 Steps 3-4 cubren los puntos conocidos; el implementador valida con grep antes. |
| Tests existentes de `loadEntrySources` fallan por el nuevo `LEFT JOIN` que devuelve count | Los tests pre-existentes no chequean `Pending`; siguen verdes mientras el resto del shape no cambie. |

Plan listo.
