# Onboarding full-screen + búsqueda unificada — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Hacer el onboarding un takeover full-screen (mobile + desktop) con skip, y unificar las dos búsquedas de `/library` en una sola caja que matchea biblioteca + catálogo AniList con agregar in-place.

**Architecture:** Item 1 es template-only: `layout.html` envuelve el chrome en bloques `html/template` overrideables y los steps de onboarding los redefinen vacíos. Item 2 amplía el handler `list` de `internal/library` para que, cuando hay query, además de filtrar entries locales busque en AniList (server-side) y arme una sección de catálogo con dedup; `/library/search` se retira a un redirect.

**Tech Stack:** Go 1.25 (stdlib `net/http`, `html/template`), SQLite (`modernc.org/sqlite`), tests con `httptest` + DB in-memory. Tuteo dominicano en todo el copy.

**Spec:** `docs/superpowers/specs/2026-06-04-onboarding-fullscreen-search-unificado-design.md`

**Branch:** `feat/onboarding-search-unificado` (ya creada; el spec está commiteado ahí).

---

## PART A — Onboarding full-screen

### Task A1: Bloques overrideables de chrome en layout.html

Refactor behavior-preserving: envolver el chrome y el footer en `{{block}}` con
el contenido actual como default. Ninguna página cambia visualmente; lo cubren
los tests de render existentes.

**Files:**
- Modify: `web/templates/layout.html:872-910`

- [ ] **Step 1: Envolver el chrome y el footer en bloques**

Reemplazar el bloque `<body>…</body>` actual (líneas 872-909) por esta versión,
que es idéntica salvo los wrappers `{{block "chrome" .}}` y `{{block "appfooter" .}}`:

```html
<body>
<div class="app-shell">
  {{block "chrome" .}}
  {{if .Session}}
  <aside class="app-sidebar">
    <a href="/" class="app-sidebar-logo">mokuji</a>
    <nav class="app-sidebar-nav">
      <a href="/library">biblioteca</a>
      <a href="/releases">releases</a>
      <a href="/settings">settings</a>
    </nav>
    <div class="app-sidebar-foot">
      <a href="/u/{{.Session.UserID}}">tu perfil</a>
      <form method="POST" action="/auth/logout" style="margin: 0; padding: 0;">
        {{csrfInput .Session}}
        <button type="submit">cerrar sesión</button>
      </form>
      <div class="app-sidebar-version">
        <a href="/version">mokuji {{appVersion}}</a> · <a href="/privacy">privacidad</a>{{if feedbackEnabled}} · <a href="/feedback">feedback</a>{{end}} · 目次
      </div>
    </div>
  </aside>
  {{end}}
  {{end}}
  <main class="app-main">
    {{block "appnav" .}}
    {{if .Session}}
    <header class="app-nav">
      <a href="/" class="app-nav-logo">mokuji</a>
      <nav>
        <a href="/library">biblioteca</a>
        <a href="/releases">releases</a>
        <a href="/settings">settings</a>
      </nav>
    </header>
    {{end}}
    {{end}}
    {{block "content" .}}<p>contenido vacío</p>{{end}}
  </main>
</div>
{{block "appfooter" .}}
<footer class="app-footer">mokuji · <a href="/version">{{appVersion}}</a> · <a href="/privacy">privacidad</a>{{if feedbackEnabled}} · <a href="/feedback">feedback</a>{{end}} · 目次</footer>
{{end}}
</body>
```

Nota: el `app-nav` (header mobile) va en su propio bloque `appnav` porque vive
dentro de `<main>`, separado del sidebar.

- [ ] **Step 2: Verificar que toda la suite sigue verde**

Run: `go test ./...`
Expected: todo `ok`. Las páginas existentes heredan el contenido default de los
bloques, así que `TestSearchRendersResults`, los tests de `home`, `profile`,
`onboarding`, etc. pasan sin cambios.

- [ ] **Step 3: Commit**

```bash
git add web/templates/layout.html
git commit -m "layout: chrome y footer en bloques overrideables"
```

---

### Task A2: Onboarding sin chrome + skip + full-screen CSS

**Files:**
- Test: `internal/onboarding/onboarding_test.go`
- Modify: `web/templates/onboarding/step1.html`
- Modify: `web/templates/onboarding/step2.html`
- Modify: `web/templates/onboarding/step3.html`

- [ ] **Step 1: Escribir el test que falla**

Agregar al final de `internal/onboarding/onboarding_test.go`:

```go
func TestStep1_FullScreenNoChromeWithSkip(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com", sql.NullInt64{}) // no onboardeado

	req := httptest.NewRequest(http.MethodGet, "/onboarding", nil)
	req = withSession(req, userID)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.Mount(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", w.Code)
	}
	body := w.Body.String()

	// Takeover full-screen: nada de chrome de app.
	for _, forbidden := range []string{"app-sidebar", "app-nav", "app-footer"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("onboarding renderiza chrome %q; debe ser full-screen", forbidden)
		}
	}
	// Skip presente, posteando a /onboarding/finish.
	if !strings.Contains(body, `action="/onboarding/finish"`) {
		t.Error("falta el form de skip a /onboarding/finish")
	}
	if !strings.Contains(body, "saltar") {
		t.Error("falta el texto de skip 'saltar'")
	}
}
```

- [ ] **Step 2: Correr el test para verlo fallar**

Run: `go test ./internal/onboarding/ -run TestStep1_FullScreenNoChromeWithSkip -v`
Expected: FAIL — el body contiene `app-sidebar`/`app-nav` (chrome del default block) y no contiene el skip todavía.

- [ ] **Step 3: step1.html — definir bloques de chrome vacíos + skip + CSS full-screen**

En `web/templates/onboarding/step1.html`, justo después de la línea
`{{define "content"}}` (línea 2), insertar las redefiniciones vacías:

```html
{{define "chrome"}}{{end}}
{{define "appnav"}}{{end}}
{{define "appfooter"}}{{end}}
```

Reemplazar el bloque de CTAs (líneas 17-19, el `<div class="onb-ctas">…</div>`) por:

```html
  <div class="onb-ctas">
    <a href="/onboarding/step2" class="onb-cta-primary">seguir →</a>
    <form method="POST" action="/onboarding/finish" class="onb-skip-form">
      {{csrfInput .Session}}
      <button type="submit" class="onb-cta-skip">saltar →</button>
    </form>
  </div>
```

En el `<style>`, reemplazar la regla `.onb { … }` por la versión full-screen y
agregar la regla del botón-skip:

```css
.onb { max-width: 30rem; margin: 0 auto; padding: 2rem 1.25rem; min-height: 100dvh; display: flex; flex-direction: column; justify-content: center; }
.onb-skip-form { margin: 0; padding: 0; }
.onb-cta-skip { background: none; border: 0; cursor: pointer; width: 100%; font-family: var(--font-mono); font-size: 0.6875rem; color: var(--text-muted); text-align: center; letter-spacing: 0.04em; padding: 0.25rem; }
.onb-cta-skip:hover { color: var(--text); }
```

- [ ] **Step 4: step2.html — mismos bloques vacíos + skip + CSS**

En `web/templates/onboarding/step2.html`, después de `{{define "content"}}` (línea 2), insertar:

```html
{{define "chrome"}}{{end}}
{{define "appnav"}}{{end}}
{{define "appfooter"}}{{end}}
```

Reemplazar el `<div class="onb-ctas">…</div>` (líneas 17-20) por:

```html
  <div class="onb-ctas">
    <a href="/onboarding/step3" class="onb-cta-primary">seguir →</a>
    <a href="/onboarding" class="onb-cta-skip">← atrás</a>
    <form method="POST" action="/onboarding/finish" class="onb-skip-form">
      {{csrfInput .Session}}
      <button type="submit" class="onb-cta-skip">saltar →</button>
    </form>
  </div>
```

En el `<style>`, reemplazar la regla `.onb { … }` por:

```css
.onb { max-width: 30rem; margin: 0 auto; padding: 2rem 1.25rem; min-height: 100dvh; display: flex; flex-direction: column; justify-content: center; }
.onb-skip-form { margin: 0; padding: 0; }
.onb-cta-skip { background: none; border: 0; cursor: pointer; width: 100%; font-family: var(--font-mono); font-size: 0.6875rem; color: var(--text-muted); text-align: center; letter-spacing: 0.04em; padding: 0.25rem; }
.onb-cta-skip:hover { color: var(--text); }
```

(La regla `.onb-cta-skip` existente para el link `← atrás` queda reemplazada por
esta, que también sirve al `<a>`.)

- [ ] **Step 5: step3.html — mismos bloques vacíos + CSS full-screen**

En `web/templates/onboarding/step3.html`, después de `{{define "content"}}` (línea 2), insertar:

```html
{{define "chrome"}}{{end}}
{{define "appnav"}}{{end}}
{{define "appfooter"}}{{end}}
```

step3 ya tiene su form de finish con el CTA `empezar →`, así que no necesita un
skip extra. Solo reemplazar la regla `.onb { … }` del `<style>` por:

```css
.onb { max-width: 30rem; margin: 0 auto; padding: 2rem 1.25rem; min-height: 100dvh; display: flex; flex-direction: column; justify-content: center; }
```

- [ ] **Step 6: Correr el test para verlo pasar**

Run: `go test ./internal/onboarding/ -run TestStep1_FullScreenNoChromeWithSkip -v`
Expected: PASS.

- [ ] **Step 7: Correr toda la suite**

Run: `go test ./...`
Expected: todo `ok`.

- [ ] **Step 8: Commit**

```bash
git add web/templates/onboarding/step1.html web/templates/onboarding/step2.html web/templates/onboarding/step3.html internal/onboarding/onboarding_test.go
git commit -m "onboarding: takeover full-screen sin chrome + botón skip"
```

---

## PART B — Búsqueda unificada en /library

### Task B1: Tipo CatalogResult + helpers de dedup

**Files:**
- Modify: `internal/library/library.go` (agregar tipo + dos helpers cerca de `listData`, ~línea 239)
- Test: `internal/library/library_test.go`

- [ ] **Step 1: Escribir los tests que fallan**

Agregar al final de `internal/library/library_test.go`:

```go
func TestDedupeCatalogMarcaInLibrary(t *testing.T) {
	results := []mangasource.SearchResult{
		{SourceID: "1", Title: "Berserk"},
		{SourceID: "2", Title: "Vagabond"},
	}
	owned := map[string]bool{"1": true}

	got := dedupeCatalog(results, owned)

	if len(got) != 2 {
		t.Fatalf("esperaba 2 resultados, obtuve %d", len(got))
	}
	if !got[0].InLibrary {
		t.Error("Berserk (source_id 1) debería estar marcado InLibrary")
	}
	if got[1].InLibrary {
		t.Error("Vagabond (source_id 2) NO debería estar marcado InLibrary")
	}
}

func TestOwnedSourceIDs(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()
	userID := insertUser(t, db, "alice@example.com")

	mangaID, err := h.upsertManga(ctx, "anilist", "30002", "Berserk", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.createEntry(ctx, userID, mangaID); err != nil {
		t.Fatal(err)
	}

	owned, err := h.ownedSourceIDs(ctx, userID, "anilist")
	if err != nil {
		t.Fatalf("ownedSourceIDs: %v", err)
	}
	if !owned["30002"] {
		t.Errorf("esperaba que 30002 estuviera en owned, got %v", owned)
	}
	if owned["99999"] {
		t.Error("99999 no debería estar en owned")
	}
}
```

- [ ] **Step 2: Correr para ver fallar (no compila: símbolos no existen)**

Run: `go test ./internal/library/ -run 'TestDedupeCatalog|TestOwnedSourceIDs'`
Expected: FAIL — `undefined: dedupeCatalog`, `h.ownedSourceIDs undefined`.

- [ ] **Step 3: Implementar el tipo y los helpers**

En `internal/library/library.go`, justo antes de `type listData struct` (línea 230), agregar:

```go
// CatalogResult es un resultado de búsqueda externa (catálogo) anotado con
// si el user ya lo tiene en su biblioteca, para el dedup del search unificado.
type CatalogResult struct {
	mangasource.SearchResult
	InLibrary bool
}

// dedupeCatalog marca cada resultado de catálogo cuyo SourceID ya esté en la
// biblioteca del user (owned), para no ofrecer "agregar" lo que ya tiene.
func dedupeCatalog(results []mangasource.SearchResult, owned map[string]bool) []CatalogResult {
	out := make([]CatalogResult, 0, len(results))
	for _, r := range results {
		out = append(out, CatalogResult{SearchResult: r, InLibrary: owned[r.SourceID]})
	}
	return out
}
```

Y junto a los demás helpers de query (después de `listEntries`, ~línea 614), agregar:

```go
// ownedSourceIDs retorna el set de source_id que el user ya tiene en su
// biblioteca para una source dada (ej. "anilist"). Sirve para deduplicar la
// sección de catálogo del search unificado.
func (h *Handler) ownedSourceIDs(ctx context.Context, userID int64, source string) (map[string]bool, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT m.source_id
		FROM library_entries le
		JOIN manga m ON m.id = le.manga_id
		WHERE le.user_id = ? AND m.source = ?
	`, userID, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owned := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		owned[id] = true
	}
	return owned, rows.Err()
}
```

- [ ] **Step 4: Correr para ver pasar**

Run: `go test ./internal/library/ -run 'TestDedupeCatalog|TestOwnedSourceIDs' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/library/library.go internal/library/library_test.go
git commit -m "library: CatalogResult + dedupeCatalog + ownedSourceIDs"
```

---

### Task B2: Handler list carga catálogo cuando hay query

**Files:**
- Modify: `internal/library/library.go` (`listData` struct + handler `list`)
- Test: `internal/library/library_test.go`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `internal/library/library_test.go`:

```go
func TestListWithQueryRendersCatalogAndDedup(t *testing.T) {
	db := setupDB(t)
	src := &fakeSource{
		results: []mangasource.SearchResult{
			{SourceID: "1", Title: "Berserk", CoverURL: "https://cdn/b.jpg", Description: "Dark fantasy"},
			{SourceID: "2", Title: "Vagabond", CoverURL: "https://cdn/v.jpg"},
		},
	}
	h := newFullHandler(t, db, src)
	userID := insertUser(t, db, "alice@example.com")

	// El user ya tiene Berserk (source_id 1) en su biblioteca.
	ctx := context.Background()
	mangaID, _ := h.upsertManga(ctx, src.Name(), "1", "Berserk", "")
	if err := h.createEntry(ctx, userID, mangaID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/library?q=ber", nil)
	req = req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID, CSRFToken: "tok123"}))
	rec := httptest.NewRecorder()
	h.list(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", rec.Code)
	}
	if src.calls != 1 {
		t.Errorf("esperaba 1 call a la source, hubo %d", src.calls)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "agregar nuevo") {
		t.Error("falta el header de la sección de catálogo")
	}
	if !strings.Contains(body, "Vagabond") {
		t.Error("falta el resultado de catálogo no-poseído (Vagabond)")
	}
	if !strings.Contains(body, "ya en tu biblioteca") {
		t.Error("Berserk (ya poseído) debería marcarse 'ya en tu biblioteca'")
	}
}

func TestListEmptyQueryNoCatalogCall(t *testing.T) {
	db := setupDB(t)
	src := &fakeSource{}
	h := newFullHandler(t, db, src)
	userID := insertUser(t, db, "alice@example.com")

	req := httptest.NewRequest(http.MethodGet, "/library", nil)
	req = req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID}))
	rec := httptest.NewRecorder()
	h.list(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", rec.Code)
	}
	if src.calls != 0 {
		t.Errorf("query vacía no debe llamar a la source; hubo %d calls", src.calls)
	}
}
```

- [ ] **Step 2: Correr para ver fallar**

Run: `go test ./internal/library/ -run 'TestListWithQueryRendersCatalog|TestListEmptyQueryNoCatalog' -v`
Expected: FAIL — el body no contiene "agregar nuevo" (el handler todavía no arma catálogo).

- [ ] **Step 3: Ampliar listData y el handler list**

En `internal/library/library.go`, agregar campos a `listData` (struct en línea 230):

```go
type listData struct {
	Session  *session.Session
	Entries  []Entry
	Filter   string
	Query    string
	UserSlug string
	// Source es el nombre de la fuente de catálogo (ej. "anilist"), usado en
	// los hidden fields del form de agregar.
	Source string
	// Catalog son los resultados de catálogo para Query (vacío si Query=="").
	Catalog []CatalogResult
	// CatalogError es el mensaje suave si la búsqueda de catálogo falla.
	CatalogError string
}
```

En el handler `list`, después de construir `entries` y el `slug` (después de la
línea ~274, antes del `h.render`), agregar el bloque de catálogo y completar el
render:

```go
	data := listData{
		Session:  s,
		Entries:  entries,
		Filter:   filter,
		Query:    query,
		UserSlug: slug.String,
	}

	if query != "" {
		data.Source = h.src.Name()
		results, err := h.src.Search(r.Context(), query)
		if err != nil {
			h.logger.Error("buscar en catálogo", "err", err, "query", query)
			data.CatalogError = "no pudimos buscar en el catálogo. prueba de nuevo."
		} else {
			owned, err := h.ownedSourceIDs(r.Context(), s.UserID, h.src.Name())
			if err != nil {
				h.logger.Error("cargar source_ids del user", "err", err)
				owned = map[string]bool{}
			}
			data.Catalog = dedupeCatalog(results, owned)
		}
	}

	h.render(w, r, "library/list.html", data)
```

Eliminar el `h.render(w, r, "library/list.html", listData{…})` anterior (líneas
276-282) que esto reemplaza.

- [ ] **Step 4: Correr para ver pasar**

Run: `go test ./internal/library/ -run 'TestListWithQueryRendersCatalog|TestListEmptyQueryNoCatalog' -v`
Expected: PASS. (Requiere que `list.html` ya renderice el catálogo — si falla por
markup faltante, completar Task B4 primero y volver. El handler está correcto.)

Nota de orden: este test depende del template de Task B4. Si se ejecuta antes,
implementar B4 y luego correr ambos. Alternativamente hacer B4 antes que el Step
4 de B2.

- [ ] **Step 5: Commit**

```bash
git add internal/library/library.go internal/library/library_test.go
git commit -m "library: list arma sección de catálogo cuando hay query"
```

---

### Task B3: add preserva query + /library/search redirige

**Files:**
- Modify: `internal/library/library.go` (handler `add` redirect, handler `search` → redirect)
- Test: `internal/library/library_test.go` (reemplazar los tests viejos de search)

- [ ] **Step 1: Escribir/actualizar los tests**

En `internal/library/library_test.go`, **reemplazar** `TestSearchRendersResults`
(líneas 377-405) y `TestSearchEmptyQueryNoCall` (líneas 407-424) por:

```go
func TestSearchRedirectsToLibraryWithQuery(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	userID := insertUser(t, db, "alice@example.com")

	req := httptest.NewRequest(http.MethodGet, "/library/search?q=berserk", nil)
	req = req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID}))
	rec := httptest.NewRecorder()
	h.search(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: esperado 303, obtenido %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/library?q=berserk" {
		t.Errorf("Location: esperado /library?q=berserk, obtenido %q", loc)
	}
}

func TestAddRedirectsPreservingQuery(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	userID := insertUser(t, db, "alice@example.com")

	form := "source=anilist&source_id=30002&title=Berserk&q=ber"
	req := httptest.NewRequest(http.MethodPost, "/library/add", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID}))
	rec := httptest.NewRecorder()
	h.add(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: esperado 303, obtenido %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/library?q=ber" {
		t.Errorf("Location: esperado /library?q=ber, obtenido %q", loc)
	}
}
```

- [ ] **Step 2: Correr para ver fallar**

Run: `go test ./internal/library/ -run 'TestSearchRedirects|TestAddRedirects' -v`
Expected: FAIL — `search` todavía renderiza 200; `add` redirige a `/library` pelado.

- [ ] **Step 3: Reescribir el handler search como redirect**

En `internal/library/library.go`, reemplazar todo el handler `search`
(líneas 293-320) por:

```go
// search es legacy: /library/search se unificó dentro de /library. Mantiene la
// ruta viva redirigiendo, para bookmarks y links externos.
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSession(w, r); !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	target := "/library"
	if q != "" {
		target = "/library?q=" + url.QueryEscape(q)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
```

Eliminar el struct `searchData` (líneas 285-291) — ya no se usa.

- [ ] **Step 4: Hacer que add preserve la query en el redirect**

En el handler `add`, reemplazar la línea final `http.Redirect(w, r, "/library", http.StatusSeeOther)` (línea ~363) por:

```go
	target := "/library"
	if q := strings.TrimSpace(r.PostFormValue("q")); q != "" {
		target = "/library?q=" + url.QueryEscape(q)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
```

- [ ] **Step 5: Asegurar el import de net/url**

Verificar que `internal/library/library.go` importe `"net/url"`. Si no está en el
bloque de imports, agregarlo.

Run: `go build ./internal/library/`
Expected: compila sin error (`url` usado y declarado).

- [ ] **Step 6: Correr los tests**

Run: `go test ./internal/library/ -run 'TestSearchRedirects|TestAddRedirects' -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/library/library.go internal/library/library_test.go
git commit -m "library: /library/search redirige + add preserva query"
```

---

### Task B4: UI unificada en list.html + retirar search.html

**Files:**
- Modify: `web/templates/library/list.html`
- Modify: `internal/library/library.go` (sacar `library/search.html` del parse loop, ~línea 175)
- Delete: `web/templates/library/search.html`

- [ ] **Step 1: Reescribir list.html con caja unificada + sección catálogo**

Reemplazar el contenido completo de `web/templates/library/list.html` por:

```html
{{define "title"}}biblioteca — mokuji{{end}}
{{define "content"}}
<header class="lib-header">
  <div class="lib-heading">
    <p class="eyebrow">Index · {{len .Entries}} {{if eq (len .Entries) 1}}entry{{else}}entries{{end}}</p>
    <h1>tu <span class="accent">biblioteca</span></h1>
  </div>
</header>

<form method="GET" action="/library" class="lib-filters" data-auto-submit>
  <input type="search" name="q" placeholder="buscar tu biblioteca o agregar manga nuevo" value="{{.Query}}">
  <select name="filter">
    <option value="all"{{if eq .Filter "all"}} selected{{end}}>todos</option>
    <option value="reading"{{if eq .Filter "reading"}} selected{{end}}>leyendo</option>
    <option value="planned"{{if eq .Filter "planned"}} selected{{end}}>por leer</option>
    <option value="completed"{{if eq .Filter "completed"}} selected{{end}}>completado</option>
  </select>
  <noscript><button type="submit">buscar</button></noscript>
</form>

{{if .Query}}<h2 class="lib-section">en tu biblioteca</h2>{{end}}

{{if .Entries}}
<ul class="manga-list">
{{range .Entries}}
  <li>
    <details>
      <summary>
        {{if .Manga.CoverURL}}<img src="{{.Manga.CoverURL}}" alt="" class="cover" width="40" loading="lazy" referrerpolicy="no-referrer">{{end}}
        <span class="status status-{{.Status}}">{{statusLabel .Status}}</span>
        {{if .IsFavorite}}<span class="favorite" title="favorito"></span>{{end}}
        <a href="/library/entry/{{.ID}}" class="manga-title-link"><strong>{{.Manga.Title}}</strong></a>
        {{if .LastChapter}}<span class="muted">— cap. {{.LastChapter}}</span>{{end}}
        {{template "mangadex-badge" .}}
      </summary>
      <form method="POST" action="/library/{{.ID}}/update" class="entry-form">
        {{ csrfInput $.Session }}
        <label>
          estado
          <select name="status">
            <option value="reading"{{if eq .Status "reading"}} selected{{end}}>leyendo</option>
            <option value="planned"{{if eq .Status "planned"}} selected{{end}}>por leer</option>
            <option value="completed"{{if eq .Status "completed"}} selected{{end}}>completado</option>
          </select>
        </label>
        <label>
          último capítulo
          <input type="text" name="last_chapter" value="{{.LastChapter}}" maxlength="100">
        </label>
        <label>
          dónde lo lees (URL)
          <input type="url" name="source_url" value="{{.SourceURL}}" maxlength="2000" placeholder="https://manhuaplus.com/...">
        </label>
        <label class="checkbox">
          <input type="checkbox" name="is_favorite"{{if .IsFavorite}} checked{{end}}>
          <span><strong>favorito</strong></span>
        </label>
        <label>
          notas
          <textarea name="notes" maxlength="5000" rows="3">{{.Notes}}</textarea>
        </label>
        <button type="submit">guardar</button>
      </form>
      {{if .SourceURL}}
      <p class="entry-source-link"><a href="{{.SourceURL}}" target="_blank" rel="noopener noreferrer">→ leer en tu fuente</a></p>
      {{end}}
      <form method="POST" action="/library/{{.ID}}/delete" onsubmit="return confirm('¿borrar {{.Manga.Title}} de tu biblioteca?');" class="delete-form">
        {{ csrfInput $.Session }}
        <button type="submit" class="danger">borrar</button>
      </form>
    </details>
  </li>
{{end}}
</ul>
{{else if .Query}}
<p class="muted lib-empty-inline">nada en tu biblioteca matchea "{{.Query}}".</p>
{{else if ne .Filter "all"}}
<div class="empty-state">
  <span class="kanji">無</span>
  <h2>0 coincidencias.</h2>
  <p>nada en tu lista matchea este filtro. prueba cambiar el estado.</p>
</div>
{{else}}
<div class="empty-state">
  <span class="kanji">白</span>
  <h2>tu índice está en blanco.</h2>
  <p>busca un manga arriba y agrégalo con tu progreso.</p>
</div>
{{end}}

{{if .Query}}
<h2 class="lib-section">agregar nuevo · {{if .Source}}{{.Source}}{{else}}anilist{{end}}</h2>
{{if .CatalogError}}<p class="error">{{.CatalogError}}</p>{{end}}
{{if .Catalog}}
<ul class="search-results">
{{range .Catalog}}
  <li class="result">
    {{if .CoverURL}}<img src="{{.CoverURL}}" alt="" class="cover" width="80" loading="lazy" referrerpolicy="no-referrer">{{end}}
    <div class="info">
      <strong>{{.Title}}</strong>
      {{if .Description}}<p class="muted">{{.Description}}</p>{{end}}
    </div>
    {{if .InLibrary}}
    <span class="muted result-owned">ya en tu biblioteca</span>
    {{else}}
    <form method="POST" action="/library/add">
      {{ csrfInput $.Session }}
      <input type="hidden" name="source" value="{{$.Source}}">
      <input type="hidden" name="source_id" value="{{.SourceID}}">
      <input type="hidden" name="title" value="{{.Title}}">
      <input type="hidden" name="cover_url" value="{{.CoverURL}}">
      <input type="hidden" name="q" value="{{$.Query}}">
      <button type="submit">+ agregar</button>
    </form>
    {{end}}
  </li>
{{end}}
</ul>
{{else if not .CatalogError}}
<div class="empty-state">
  <span class="kanji">無</span>
  <h2>nada en el catálogo.</h2>
  <p>prueba kanji, romaji, o el título en inglés. AniList indexa con varios alias.</p>
</div>
{{end}}
{{end}}

<p class="user-links">
  <a href="/u/{{if .UserSlug}}{{.UserSlug}}{{else}}{{.Session.UserID}}{{end}}">tu vista pública</a>
</p>
{{end}}
```

(El form de logout al pie se retira de aquí — ya vive en el sidebar/footer del
chrome. La caja unificada reemplaza tanto el filtro viejo como el link "+ agregar
manga".)

- [ ] **Step 2: Agregar estilos de las secciones al `<style>` de layout.html**

En `web/templates/layout.html`, dentro del `<style>`, agregar cerca de los estilos
de `.lib-header` / `.search-results` (buscar `.search-results` para ubicar la zona):

```css
.lib-section { font-family: var(--font-mono); font-size: 0.75rem; letter-spacing: 0.1em; text-transform: uppercase; color: var(--text-muted); margin: 1.75rem 0 0.75rem; }
.lib-empty-inline { margin: 0.5rem 0 0; }
.result-owned { font-family: var(--font-mono); font-size: 0.6875rem; letter-spacing: 0.04em; white-space: nowrap; }
```

- [ ] **Step 3: Sacar search.html del parse loop**

En `internal/library/library.go`, línea ~175, cambiar el slice de páginas para
quitar `"library/search.html"`:

```go
	for _, page := range []string{"library/list.html", "library/entry.html"} {
```

- [ ] **Step 4: Borrar el template search.html**

```bash
git rm web/templates/library/search.html
```

- [ ] **Step 5: Build + correr toda la suite de library**

Run: `go test ./internal/library/ -v`
Expected: todo PASS, incluyendo `TestListWithQueryRendersCatalogAndDedup` (que
depende de este markup) y los redirects de Task B3.

- [ ] **Step 6: Correr toda la suite del repo**

Run: `go test ./...`
Expected: todo `ok`.

- [ ] **Step 7: Commit**

```bash
git add web/templates/library/list.html web/templates/layout.html internal/library/library.go web/templates/library/search.html
git commit -m "library: search unificado en /library + retira search.html"
```

---

## Verificación final (manual, con `make run`)

- [ ] `make run`, login, ir a `/onboarding` en desktop ancho: sin sidebar, full-screen; el skip cae en `/library`. Repetir angostando la ventana a mobile: sin nav top ni footer.
- [ ] En `/library`, escribir un título que tengas y uno que no: aparece sección "en tu biblioteca" + "agregar nuevo"; el que ya tienes sale "ya en tu biblioteca", el otro con "+ agregar".
- [ ] Agregar uno: vuelves a `/library?q=…` (sigues en contexto), el manga se mueve a "en tu biblioteca".
- [ ] Navegar a `/library/search?q=x`: redirige a `/library?q=x`.
- [ ] Verificar que `/releases`, `/settings`, landing y `/auth` siguen mostrando el chrome igual que antes (regresión del block override).

---

## Self-review (cubierto)

- **Onboarding full-screen mobile+desktop**: Task A1 (bloques) + A2 (defines vacíos `chrome`/`appnav`/`appfooter` + CSS `100dvh`). ✓
- **Skip**: A2 (form POST a `/onboarding/finish`, que ya defaultea weekly). ✓
- **Búsqueda unificada server-side + dedup**: B1 (helpers) + B2 (handler) + B4 (UI). ✓
- **Copy señala ambos scopes**: B4 (placeholder "buscar tu biblioteca o agregar manga nuevo" + headers de sección). ✓
- **Agregar in-place / preserva contexto**: B3 (add redirige a `/library?q=`). ✓
- **/library/search retirado**: B3 (redirect) + B4 (borra template + parse loop). ✓
- **Error de AniList no rompe la lista**: B2 (`CatalogError`, entries se renderizan igual). ✓
```
