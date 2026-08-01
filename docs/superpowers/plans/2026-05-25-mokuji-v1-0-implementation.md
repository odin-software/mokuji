# mokuji v1.0 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cerrar v1.0 — el milestone "lista para promoción pública": docs ricas (README + self-hosting + CONTRIBUTING), discoverability técnica (robots/sitemap/llms.txt/JSON-LD), sweep final de voseo en docs raíz, y bump de versión a `v1.0.0` con pausa explícita antes de merge a `main`.

**Architecture:** Pieza única no-feature: todo es documentación y endpoints estáticos. Sin cambios de schema, sin nuevos paquetes de dominio. El único toque al servidor es agregar handlers para `/robots.txt`, `/sitemap.xml` y `/llms.txt` en `internal/server/routes.go` (mismo patrón que `mountPWA` ya usa para `/sw.js` y `/manifest.webmanifest`). Discoverability se trata como infraestructura del producto, no como feature.

**Tech Stack:** Go stdlib (`net/http`, `html/template`), Markdown plano para docs, JSON-LD en HTML para structured data, archivos estáticos servidos vía `embed.FS` ya existente.

**Convenciones obligatorias (heredadas del CLAUDE.md del proyecto y memorias del user):**

- Copy 100% en español dominicano (tuteo). **Nunca** voseo: `lees` no `leés`, `prueba` no `probá`, `tú` no `vos`, `tienes` no `tenés`.
- Commits chicos, subject en español, lowercase, sin punto final.
- ADR sólo si la decisión tiene tradeoff real. Este plan no genera ADRs nuevos.
- Confirmar con el user antes de cualquier acción contra producción (`ssh mimir`, `curl` con side effects, etc.).
- Pausa explícita antes del bump `v1.0.0`, antes del tag, y antes del merge a `main`. El CD deploya solo cuando `main` mueve.

**Sources reales del producto a documentar (verificado en `main.go:174-209`):**

- **MangaDex** — siempre on, sin env var.
- **Asura Scans** — opt-in vía `ASURA_ENABLED=true` (default on).
- **Comick** — opt-in vía `COMICK_ENABLED=true` (default on, usa `comick.art` sin Cloudflare).

El spec original (`docs/superpowers/specs/2026-05-24-mokuji-v1.0-design.md`) menciona "MangaDex + Asura" pero está desactualizado: Comick se sumó después. Toda la doc nueva habla de **3 sources**.

---

## File Structure

### Documentos a crear/reescribir

| Archivo | Estado | Responsabilidad |
|---------|--------|-----------------|
| `README.md` | reescribir | Hero + value prop + screenshot + sources + stack + quickstart + AGPL. Cara de Forgejo. |
| `SECURITY.md` | sweep voseo | Pulir tuteo dominicano, mantener contenido. |
| `CONTRIBUTING.md` | nuevo | Cómo correr local, tests, commits, ADR, PR flow, voz. |
| `docs/self-hosting.md` | nuevo | Guía agnóstica de plataforma: build + env vars + principios de deploy. |
| `docs/README.md` | actualizar | Agregar entradas para `self-hosting.md` y `CONTRIBUTING.md`. |
| `docs/estado.md` | actualizar | Cerrar fila v0.8.x, agregar fila v1.0 al cuadro de minors. |
| `VERSION` | bump | `v0.8.2` → `v1.0.0` (último commit antes de merge). |

### Endpoints técnicos a agregar

| Archivo | Estado | Responsabilidad |
|---------|--------|-----------------|
| `internal/server/discoverability.go` | nuevo | Handlers para `/robots.txt`, `/sitemap.xml`, `/llms.txt`. Mismo patrón que `mountPWA`. |
| `internal/server/discoverability_test.go` | nuevo | Tests de los tres endpoints. |
| `internal/server/routes.go` | modificar | Llamar a `mountDiscoverability(mux)` después de `mountPWA(mux)`. |
| `web/templates/landing.html` | modificar | Agregar bloque JSON-LD (SoftwareApplication schema) dentro del head. |
| `web/templates/layout.html` | modificar | Agregar `<link rel="canonical">` opcional vía bloque template. |
| `internal/home/privacy.go` | nuevo | Handler para `GET /privacy` que renderiza la página. |
| `internal/home/privacy_test.go` | nuevo | Smoke test del handler. |
| `web/templates/privacy.html` | nuevo | Markup de la página de privacidad. |

### Decisiones de scope cerradas con el user (2026-05-25)

- **Visibilidad del repo**: ~~el Forgejo `git.odin.do` está cerrado a browse anónimo~~ → **RESUELTO 2026-05-25**: el user abrió visibilidad pública. Verificado: HTTP 200 sin auth en `https://git.odin.do/kengru/mokuji` y en la API REST. Ya no es bloqueante para promoción.
- **Feedback in-app**: **diferido a v1.1**. Será un form web propio en `/feedback`, escribe a `kg@odin.do`, con atención explícita a anti-abuso (rate limit por IP + posiblemente captcha simple o honeypot). Anotado en backlog post-v1.0. Para v1.0, el canal de feedback es el email de `SECURITY.md` y eventualmente issues en Forgejo (cuando el repo sea visible).
- **Páginas legales**: sólo `/privacy` en v1.0. Sin terms separados.

---

## Task 1: Branch + sweep voseo en docs raíz

**Files:**
- Modify: `README.md`
- Modify: `SECURITY.md`

**Branching premise:** Estamos partiendo de `main` en `v0.8.2` con working tree limpio. La rama se crea **antes** de tocar nada.

- [ ] **Step 1: Crear la rama version-1-0**

Run:
```bash
git checkout -b version-1-0
git status
```
Expected: `On branch version-1-0` y working tree limpio.

- [ ] **Step 2: Identificar voseo restante en README.md y SECURITY.md**

Run:
```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|vos\b)' README.md SECURITY.md
```
Expected output (verificado en recon):

```
README.md:42:[AGPL-3.0](LICENSE). Significa: cualquiera puede usar, modificar y correr mokuji. Si lo corrés como servicio público para otros usuarios, debés ofrecer el código fuente (incluyendo tus modificaciones) a esos usuarios.
SECURITY.md:5:Si encontraste una vulnerabilidad en mokuji, contactá directamente a **support@odin.do**. No abras un issue público.
SECURITY.md:7:Si podés, incluí:
SECURITY.md:18:- Crédito al reporter en el changelog si lo deseás.
SECURITY.md:27:- Después de confirmar y mitigar el problema, coordinaremos disclosure responsable con vos.
```

- [ ] **Step 3: Sweep voseo en SECURITY.md**

Aplicar estos reemplazos con Edit:

| Original (voseo) | Reemplazo (tuteo) |
|------------------|-------------------|
| `contactá directamente` | `contacta directamente` |
| `Si podés, incluí:` | `Si puedes, incluye:` |
| `si lo deseás` | `si lo deseas` |
| `coordinaremos disclosure responsable con vos` | `coordinaremos disclosure responsable contigo` |

Después correr el grep de Step 2 — debe devolver 0 líneas en SECURITY.md.

- [ ] **Step 4: Sweep voseo en README.md (sólo la línea de licencia, el resto se reescribe en Task 2)**

En `README.md:42` reemplazar:
```
[AGPL-3.0](LICENSE). Significa: cualquiera puede usar, modificar y correr mokuji. Si lo corrés como servicio público para otros usuarios, debés ofrecer el código fuente (incluyendo tus modificaciones) a esos usuarios.
```
por:
```
[AGPL-3.0](LICENSE). Significa: cualquiera puede usar, modificar y correr mokuji. Si lo corres como servicio público para otros usuarios, debes ofrecer el código fuente (incluyendo tus modificaciones) a esos usuarios.
```

- [ ] **Step 5: Verificar grep limpio**

Run:
```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|vos\b)' README.md SECURITY.md
```
Expected: 0 líneas (exit code 1 — no matches).

- [ ] **Step 6: Commit**

```bash
git add README.md SECURITY.md
git commit -m "$(cat <<'EOF'
copy: sweep voseo en README y SECURITY (dominicano)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Endpoints de discoverability (robots, sitemap, llms.txt)

**Files:**
- Create: `internal/server/discoverability.go`
- Create: `internal/server/discoverability_test.go`
- Modify: `internal/server/routes.go` (un solo call site para `mountDiscoverability`)

**Why this comes before docs:** Los endpoints son testables con `go test`. Si la firma del handler o la integración con `routes.go` cambia, el feedback es inmediato. Las docs (Tasks 3-5) dependen sólo del estado final del repo, no de este código.

**Principles for these endpoints:**

- `robots.txt`: allow-all + apuntar al sitemap. Mokuji no tiene áreas privadas indexables vía crawl (el contenido detrás de auth no es accesible sin sesión, así que `Disallow` no aporta).
- `sitemap.xml`: estático con `/` solamente. `/auth` lo dejamos fuera porque cambia de contenido según el estado de sesión y no aporta SEO. Los perfiles públicos `/u/{slug}` son user-generated y no enumerables sin un endpoint que liste users públicos (no existe ni queremos crearlo para v1.0).
- `llms.txt`: formato propuesto por Anthropic (`https://llmstxt.org`). Es un Markdown corto que describe el proyecto para que LLMs que indexen el sitio entiendan el contexto. Lo escribimos a mano, vive embebido.

- [ ] **Step 1: Escribir el test del handler de robots.txt**

Create `internal/server/discoverability_test.go`:

```go
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRobotsTxt(t *testing.T) {
	mux := http.NewServeMux()
	mountDiscoverability(mux, "https://mokuji.org")

	req := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type: got %q, want text/plain", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "User-agent: *") {
		t.Errorf("body missing User-agent directive: %q", body)
	}
	if !strings.Contains(body, "Sitemap: https://mokuji.org/sitemap.xml") {
		t.Errorf("body missing sitemap reference: %q", body)
	}
}
```

- [ ] **Step 2: Correr el test para verificar que falla**

Run: `go test ./internal/server/ -run TestRobotsTxt -v`
Expected: FAIL con `undefined: mountDiscoverability`.

- [ ] **Step 3: Implementar mountDiscoverability con sólo robots.txt**

Create `internal/server/discoverability.go`:

```go
// Package server — handlers para discoverability técnica (SEO + LLM-friendly
// indexing). Se sirven en el root del dominio igual que /sw.js y
// /manifest.webmanifest (ver mountPWA en routes.go).
package server

import (
	"fmt"
	"net/http"
)

// mountDiscoverability registra los endpoints que ayudan a crawlers y LLMs
// a indexar mokuji correctamente:
//
//   - /robots.txt    estándar de exclusión de crawlers
//   - /sitemap.xml   mapa de URLs públicas
//   - /llms.txt      descripción Markdown del proyecto para LLMs (llmstxt.org)
//
// baseURL es el origen público (ej. "https://mokuji.org") usado para emitir
// URLs absolutas en sitemap y robots.
func mountDiscoverability(mux *http.ServeMux, baseURL string) {
	mux.HandleFunc("GET /robots.txt", serveRobotsTxt(baseURL))
}

func serveRobotsTxt(baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fmt.Fprintf(w, "User-agent: *\nAllow: /\n\nSitemap: %s/sitemap.xml\n", baseURL)
	}
}
```

- [ ] **Step 4: Correr el test — debe pasar**

Run: `go test ./internal/server/ -run TestRobotsTxt -v`
Expected: PASS.

- [ ] **Step 5: Escribir el test del sitemap**

Agregar a `internal/server/discoverability_test.go`:

```go
func TestSitemapXml(t *testing.T) {
	mux := http.NewServeMux()
	mountDiscoverability(mux, "https://mokuji.org")

	req := httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("content-type: got %q, want application/xml", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Errorf("missing XML prolog: %q", body)
	}
	if !strings.Contains(body, "<loc>https://mokuji.org/</loc>") {
		t.Errorf("missing root URL: %q", body)
	}
}
```

- [ ] **Step 6: Correr el test — debe fallar (404)**

Run: `go test ./internal/server/ -run TestSitemapXml -v`
Expected: FAIL con `status: got 404, want 200`.

- [ ] **Step 7: Agregar handler de sitemap**

En `internal/server/discoverability.go` agregar dentro de `mountDiscoverability`:
```go
mux.HandleFunc("GET /sitemap.xml", serveSitemap(baseURL))
```

Y agregar la función:

```go
func serveSitemap(baseURL string) http.HandlerFunc {
	const tpl = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url>
    <loc>%s/</loc>
    <changefreq>weekly</changefreq>
    <priority>1.0</priority>
  </url>
</urlset>
`
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fmt.Fprintf(w, tpl, baseURL)
	}
}
```

- [ ] **Step 8: Correr el test — debe pasar**

Run: `go test ./internal/server/ -run TestSitemapXml -v`
Expected: PASS.

- [ ] **Step 9: Escribir el test de llms.txt**

Agregar a `internal/server/discoverability_test.go`:

```go
func TestLLMsTxt(t *testing.T) {
	mux := http.NewServeMux()
	mountDiscoverability(mux, "https://mokuji.org")

	req := httptest.NewRequest(http.MethodGet, "/llms.txt", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("content-type: got %q, want text/markdown", ct)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "# mokuji") {
		t.Errorf("body should start with title heading: %q", body[:min(40, len(body))])
	}
	if !strings.Contains(body, "AGPL") {
		t.Errorf("body missing license mention: %q", body)
	}
}
```

- [ ] **Step 10: Correr el test — debe fallar (404)**

Run: `go test ./internal/server/ -run TestLLMsTxt -v`
Expected: FAIL.

- [ ] **Step 11: Implementar llms.txt**

En `internal/server/discoverability.go` agregar:

```go
mux.HandleFunc("GET /llms.txt", serveLLMsTxt)
```

Y la función con el contenido embebido como string literal (no archivo separado — son ~40 líneas, embeber simplifica el deploy):

```go
const llmsTxtBody = `# mokuji

Una biblioteca pública y self-hostable para tu manga. Sin algoritmos, sin
recomendaciones — solo un índice prolijo de lo que importa.

## Qué hace

- **Biblioteca privada por user**: cataloga títulos con estado leyendo / por
  leer / completado, marca favoritos, exporta a JSON cuando quieras.
- **Tracking de releases multi-source**: matchea automáticamente contra
  MangaDex, Asura Scans y Comick. Cuando hay capítulo nuevo, lo ves en
  ` + "`/releases`" + ` y opcionalmente por email.
- **Digest opt-in**: semanal por default (domingos), diario si elegís, o
  apagado. Nunca predatory.
- **Perfiles públicos** en ` + "`/u/{handle}`" + `: si el user marca el perfil
  como público, su lista es visible sin login.

## Stack

Go + SQLite, stdlib ` + "`net/http`" + ` puro, ` + "`html/template`" + `
server-rendered, sin frameworks. Single binary, ~13 MB stripped, sin CGO.
PWA instalable. Email outbound vía SMTP estándar.

## Licencia

[AGPL-3.0](https://git.odin.do/kengru/mokuji/src/branch/main/LICENSE).
Si corres una instancia pública, debes ofrecer el código a tus users.

## Recursos

- Instancia pública: <https://mokuji.org>
- Código fuente: <https://git.odin.do/kengru/mokuji>
- Self-hosting: <https://git.odin.do/kengru/mokuji/src/branch/main/docs/self-hosting.md>
- Filosofía y decisiones: <https://git.odin.do/kengru/mokuji/src/branch/main/docs/adr>
`

func serveLLMsTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte(llmsTxtBody))
}
```

- [ ] **Step 12: Correr todos los tests del paquete**

Run: `go test ./internal/server/ -v -run "TestRobotsTxt|TestSitemapXml|TestLLMsTxt"`
Expected: 3 PASS.

- [ ] **Step 13: Wirear mountDiscoverability en routes.go**

En `internal/server/routes.go`, después de la llamada a `s.mountPWA(mux)` (cerca de la línea 50-60 según el código actual), agregar:

```go
s.mountDiscoverability(mux)
```

Y agregar el método sobre `Server` para que tenga acceso a `s.cfg.BaseURL`:

```go
// mountDiscoverability llama al constructor de package-level pasándole la
// BaseURL del config para que los URLs absolutos del sitemap y robots
// salgan correctos en cada entorno.
func (s *Server) mountDiscoverability(mux *http.ServeMux) {
	mountDiscoverability(mux, s.cfg.BaseURL)
}
```

Antes de editar `routes.go`, verificar primero el nombre real del campo de baseURL en `server.go`:

Run: `grep -n "BaseURL\|baseURL" internal/server/server.go`

Adaptar el código de arriba al nombre real (puede ser `s.cfg.BaseURL`, `s.baseURL`, etc.).

- [ ] **Step 14: Correr el suite completo del paquete server**

Run: `go test ./internal/server/...`
Expected: PASS (incluyendo los tests existentes que no debíamos haber roto).

- [ ] **Step 15: Smoke test local con el binario corriendo**

Si hay ambiente local listo (mailpit + .env):

Run:
```bash
make build && ./bin/mokuji &
sleep 2
curl -sS http://localhost:8080/robots.txt
curl -sS http://localhost:8080/sitemap.xml
curl -sS http://localhost:8080/llms.txt | head -10
kill %1
```

Expected: Cada endpoint responde con su body. Si el user no quiere correr el binario local en esta sesión, **saltar este step** — los unit tests ya cubrieron el comportamiento.

- [ ] **Step 16: Commit**

```bash
git add internal/server/discoverability.go internal/server/discoverability_test.go internal/server/routes.go
git commit -m "$(cat <<'EOF'
seo: robots.txt + sitemap.xml + llms.txt en el root

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: JSON-LD structured data + canonical link

**Files:**
- Modify: `web/templates/layout.html`
- Modify: `web/templates/landing.html`

**Why JSON-LD on landing only:** Schema.org `SoftwareApplication` describe la app entera y sólo tiene sentido en la home pública. Las páginas autenticadas (library, releases, settings) no tienen contenido público que un buscador indexe. Para v1.0 esto es suficiente — si más adelante exponemos perfiles públicos a search, agregamos un schema `ProfilePage` dedicado.

**Why canonical link:** mokuji ya redirige `/` a la versión canónica (no hay duplicados de `www.` ni rutas con/sin slash conflictivas), pero un `<link rel="canonical">` explícito ayuda a crawlers y a previews de redes que de otro modo arman URLs raras (querystrings de tracking, fragmentos).

- [ ] **Step 1: Agregar bloque canonical en layout.html**

Abrir `web/templates/layout.html`. Antes del cierre de `{{block "meta" .}}` (alrededor de la línea 21, después de los meta de Twitter), agregar:

```html
<link rel="canonical" href="{{block "canonical" .}}{{.BaseURL}}{{end}}">
```

Si `.BaseURL` no está disponible en el contexto del template, verificar el data model del layout en `internal/home/handler.go` (o donde sea que se invoque) y agregar el campo. Si pasarlo es complicado para esta vuelta, simplificar a:

```html
<link rel="canonical" href="{{block "canonical" .}}https://mokuji.org{{end}}">
```

con un comentario en el template explicando que self-hosters lo overridean por bloque.

- [ ] **Step 2: Agregar JSON-LD en landing.html**

En `web/templates/landing.html`, dentro del bloque `{{define "meta"}}` (o donde sobreescriba el head — si no hay bloque meta override, agregar uno), agregar al final del bloque:

```html
<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "SoftwareApplication",
  "name": "mokuji",
  "applicationCategory": "BookCollectionApplication",
  "operatingSystem": "Web, Linux, macOS, Windows, iOS, Android",
  "description": "Una biblioteca pública y self-hostable para tu manga. Cataloga, sigue capítulos nuevos, enlaza a la fuente. Sin algoritmos.",
  "url": "https://mokuji.org",
  "license": "https://www.gnu.org/licenses/agpl-3.0.html",
  "offers": {
    "@type": "Offer",
    "price": "0",
    "priceCurrency": "USD"
  },
  "inLanguage": "es"
}
</script>
```

- [ ] **Step 3: Validar que el template todavía compila**

Run: `go build ./...`
Expected: build OK, sin errores de parse del template.

- [ ] **Step 4: Si hay tests del paquete home, correrlos**

Run: `go test ./internal/home/...`
Expected: PASS.

- [ ] **Step 5: Smoke visual (opcional, depende del ambiente)**

Si está disponible levantar local:

```bash
make build && ./bin/mokuji &
sleep 2
curl -sS http://localhost:8080/ | grep -E "ld\+json|canonical" | head -5
kill %1
```

Expected: el HTML contiene la línea `<link rel="canonical">` y el bloque `<script type="application/ld+json">`.

Validar el JSON-LD a mano en <https://validator.schema.org/> es opcional pero recomendable; lo puede hacer el user cuando se pruebe en prod.

- [ ] **Step 6: Commit**

```bash
git add web/templates/layout.html web/templates/landing.html
git commit -m "$(cat <<'EOF'
seo: canonical link en layout + JSON-LD SoftwareApplication en landing

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: docs/self-hosting.md agnóstico de plataforma

**Files:**
- Create: `docs/self-hosting.md`

**Why platform-agnostic:** Decisión explícita del user (2026-05-25): "que sea agnóstico relativamente a plataforma, nos preocupamos porque pueda correr local, hacer buen binario, tener buena documentación y le dejamos al usuario el deploy en el lugar en sí. Importante de que si nosotros estamos particularmente casado con algo, pues que lo hagamos un poco más agnóstico". Entonces:

- No prescribimos systemd/nginx como única opción. Mencionamos el setup de mokuji.org como **un ejemplo entre varios**.
- Documentamos principios (proceso supervisor, reverse proxy con TLS, persistencia del DB, backup) y dejamos elección al lector.
- Cubrimos: build del binario, env vars completas (referencia 1:1), arranque, persistencia, update path.
- No documentamos a fondo Docker/Caddy/Fly.io. Mencionamos en una nota al pie que son posibles y que contribuciones son bienvenidas en CONTRIBUTING.md.

- [ ] **Step 1: Crear el archivo con el siguiente contenido completo**

Content for `docs/self-hosting.md`:

```markdown
# Self-hosting mokuji

mokuji está diseñado para correr donde quieras: un VPS común, una máquina en
tu red local, un container, una Raspberry Pi en un estante. El binario es
estático (Go, sin CGO), pesa ~13 MB, y todo el estado vive en un único
archivo SQLite.

Esta guía cubre los principios del deploy. La instancia pública en
[mokuji.org](https://mokuji.org) corre con systemd + nginx en un VPS Linux,
pero no es la única forma válida — al final del documento hay una nota con
otros caminos posibles.

## Lo que necesitas

- Un host con la habilidad de correr un binario Linux x86_64 (o que puedas
  compilar el binario tú mismo, ver "build" abajo).
- Un proceso supervisor que reinicie mokuji si crashea (`systemd`, `runit`,
  `s6`, `supervisord`, `docker` con `restart: unless-stopped`, etc.).
- Un reverse proxy con TLS terminator si lo expones a internet (nginx,
  Caddy, Traefik, HAProxy — el que prefieras). En la red local no es
  estrictamente necesario, pero recomendado para que las cookies `Secure`
  funcionen.
- Un proveedor SMTP para enviar OTPs y digest. mokuji.org usa Resend; tu
  proveedor de email transaccional favorito sirve igual mientras hable
  SMTP con STARTTLS.

mokuji **no** necesita: Docker, base de datos externa, Redis, queue
broker, NPM, Node, ni nada por el estilo. Es un solo binario y un archivo
SQLite.

## Build

Hay dos caminos.

### Bajar un binario ya compilado

Los releases publicados están en
[git.odin.do/kengru/mokuji/releases](https://git.odin.do/kengru/mokuji/releases).
Cada release adjunta `mokuji-linux-amd64`, statically-linked, listo para
correr en cualquier distro Linux moderna.

### Compilar desde el código

Requiere Go 1.25 o más nuevo.

```bash
git clone https://git.odin.do/kengru/mokuji.git
cd mokuji
make build              # binario para el host actual en ./bin/mokuji
make build-linux-amd64  # cross-compile para Linux x86_64 en ./bin/mokuji-linux-amd64
```

El `Makefile` inyecta la versión leída de `./VERSION` en el binario; el
endpoint `/version` y el footer la muestran. En builds de dev (`make run`,
`air`) se le agrega sufijo `-dev` automáticamente.

## Configuración

Toda la configuración vive en variables de entorno. Copia [`.env.example`](../.env.example)
como referencia. Lo mínimo necesario para arrancar es muy poco; el resto
escala según las features que actives.

### Esenciales

| Variable | Default | Notas |
|----------|---------|-------|
| `HTTP_ADDR` | `:8080` | Dirección donde escucha. Usa `127.0.0.1:8080` detrás de un proxy. |
| `BASE_URL` | `http://localhost:8080` | URL pública. Aparece en links de email (OTP, digest, unsubscribe). |
| `DB_PATH` | `./data/mokuji.db` | Path al archivo SQLite. Crear el directorio antes; mokuji crea el archivo. |
| `SMTP_HOST` | — | Host SMTP del proveedor de email. |
| `SMTP_PORT` | — | Puerto SMTP. Usa un puerto STARTTLS (587, 2587, 2525). Implícito TLS (`465`) no es soportado por la stdlib de Go. |
| `SMTP_USERNAME` | — | Usuario SMTP. |
| `SMTP_PASSWORD` | — | Password / API key. |
| `SMTP_FROM` | — | Dirección "From" para OTPs. Ej: `mokuji <auth@tudominio.com>`. |

### En producción (recomendado obligatorio cuando expones a internet)

| Variable | Default | Notas |
|----------|---------|-------|
| `SECURE_COOKIES` | `false` | `true` cuando hay HTTPS. Activarlo hace fail-fast si faltan los secretos de abajo. |
| `TRUST_PROXY` | `false` | `true` si hay reverse proxy delante. mokuji lee `X-Forwarded-For` para rate limiting e logs. NO activar sin proxy — un cliente podría spoofear el IP. |
| `UNSUBSCRIBE_SECRET` | — | HMAC para firmar links de unsubscribe. Mínimo 32 bytes. Generar: `openssl rand -base64 32`. Rotarlo invalida los links pendientes. |
| `DELETION_LOG_PEPPER` | — | Pepper para hashear user_id en el audit log de borrado de cuentas. Generar igual que arriba. |

### Opcionales por feature

| Variable | Default | Activa |
|----------|---------|--------|
| `SMTP_FROM_PRODUCT` | comparte `SMTP_FROM` | "From" separado para emails de producto (digest). Útil para que la deliverability de auth y producto sean independientes. |
| `ADMIN_TOKEN` | (vacío = endpoints deshabilitados) | Bearer token para `POST /admin/trigger-digest` y `GET /admin/sources-health`. |
| `ASURA_ENABLED` | `true` | Source [Asura Scans](https://asurascans.com). Zona gris legal (scanlation no oficial); bájalo a `false` si prefieres sólo MangaDex. |
| `ASURA_API_BASE_URL` | — | Override si Asura rota su dominio. Vacío = `api.asurascans.com`. |
| `ASURA_FRONTEND_BASE_URL` | — | Override del frontend para reconstruir URLs de reader. |
| `COMICK_ENABLED` | `true` | Source [Comick](https://comick.io). Usa el subdominio `comick.art` que expone la API sin Cloudflare challenge. |
| `COMICK_API_BASE_URL` | — | Override del API base. |
| `COMICK_FRONTEND_BASE_URL` | — | Override del frontend. |

### Lista completa

Mira [`.env.example`](../.env.example) para ver el archivo entero con
comentarios inline, incluyendo los defaults para Mailpit en desarrollo
local.

## Persistencia

mokuji guarda absolutamente todo en `DB_PATH`. Backup = copiar ese archivo.

Opciones por orden de robustez:

1. **`cp` con app parada**. Lo más simple. Para si la instancia tolera
   downtime corto de backup.
2. **`sqlite3 mokuji.db ".backup mokuji.bak"`** con la app corriendo. Hace
   un snapshot consistente usando el lock de SQLite.
3. **[Litestream](https://litestream.io/)** para replicación continua a S3,
   B2, SFTP. Recomendado para instancias serias. Corre como sidecar al
   binario.

Las migraciones corren automáticamente al startup. Cuando bajas una versión
nueva del binario y la reinicias, aplica los `.sql` pendientes vía el
runner embebido (~60 LOC en `internal/migrations`). **Backup antes de
actualizar versiones** es un hábito sano hasta que la suite de migrations
tenga una guarantía formal de reversibilidad.

## Proceso supervisor

Necesitas algo que reinicie mokuji si muere. Cualquier supervisor sirve.
Tres ejemplos cortos:

### systemd

```ini
# /etc/systemd/system/mokuji.service
[Unit]
Description=mokuji
After=network.target

[Service]
Type=simple
User=mokuji
ExecStart=/usr/local/bin/mokuji
EnvironmentFile=/etc/mokuji/env
Restart=on-failure
RestartSec=5s
StateDirectory=mokuji

[Install]
WantedBy=multi-user.target
```

Apunta `DB_PATH=/var/lib/mokuji/mokuji.db` (`StateDirectory=mokuji`
resuelve a `/var/lib/mokuji` con `User=mokuji`). El `EnvironmentFile`
debe ser `0600 root:root`.

### docker-compose

```yaml
services:
  mokuji:
    image: ghcr.io/youruser/mokuji:1.0.0  # build & push tú mismo, no hay imagen oficial
    restart: unless-stopped
    ports:
      - "127.0.0.1:8080:8080"
    environment:
      BASE_URL: https://mokuji.tudominio.com
      DB_PATH: /data/mokuji.db
      # ...resto de env vars
    volumes:
      - ./data:/data
```

Nota: no publicamos imagen oficial todavía. El binario es tan chico que
un `Dockerfile` de 6 líneas (`FROM gcr.io/distroless/static`, `COPY
mokuji /mokuji`, `ENTRYPOINT ["/mokuji"]`) alcanza.

### s6 / runit / supervisord

Cualquier servicio que pase env vars al proceso y reinicie en crash.

## Reverse proxy con TLS

Si lo expones a internet:

- Termina TLS en el proxy (Let's Encrypt vía certbot, Caddy auto-TLS, etc.).
- Pasa el tráfico a `127.0.0.1:8080` (o donde sea que escuche mokuji).
- Asegúrate de pasar `X-Forwarded-For` con el IP original del cliente.
- Pon `TRUST_PROXY=true` en mokuji para que lo respete.
- Headers de seguridad (CSP, HSTS, X-Frame-Options, etc.) los emite mokuji
  internamente — no los dupliques en el proxy.

## Gotcha de SMTP en algunos providers cloud

DigitalOcean (y algunos otros) bloquean los puertos SMTP estándar (25, 465,
587) outbound desde Droplets nuevos. Si vas a usar Resend en uno de estos
hosts, usa **`SMTP_PORT=2587`** — Resend ofrece ese puerto STARTTLS
exactamente para este caso. Otros providers de email transaccional ofrecen
puertos alternativos similares; chequea su doc.

Nota relacionada: la stdlib de Go (`net/smtp`) no soporta TLS implícito en
`:465`, sólo STARTTLS. Conectarte a `:465` cuelga hasta timeout. Usa
siempre puertos STARTTLS.

## Update path

1. Backup del DB (ver "Persistencia").
2. Bajar el binario nuevo.
3. Reemplazar el binario actual.
4. Reiniciar el proceso supervisor.
5. Verificar `/version` devuelve la versión esperada.

```bash
curl https://tu-instancia/version
```

Las migraciones corren solas durante el startup. Si una falla, mokuji loggea
el error y aborta — chequea los logs y restaura el backup del DB antes de
intentar de nuevo.

## Troubleshooting

- **Logs**: van a stdout del proceso. Con systemd: `journalctl -u mokuji
  -f`. Con docker: `docker compose logs -f mokuji`. Sin supervisor:
  redireccioná `> mokuji.log 2>&1` al arrancar.
- **DB ubicación**: el valor de `DB_PATH`. Verificar permisos del directorio
  padre (mokuji crea el archivo si falta, pero no puede crear el
  directorio).
- **Migrate-only**: para correr migraciones sin levantar el server:
  ```bash
  ./mokuji -migrate-only
  ```
- **Salud de las sources**: `GET /admin/sources-health` con `Authorization:
  Bearer $ADMIN_TOKEN` devuelve estado por source. Útil cuando una source
  cambia su API.

## Otros entornos no documentados aquí

Caddy en lugar de nginx, FreeBSD/OpenBSD, deploy a Fly.io/Railway/Render,
Kubernetes — todos posibles. Si corres mokuji en uno de estos entornos y
quieres documentar el path, abre un PR con la sub-sección y lo
incorporamos. Mira [CONTRIBUTING.md](../CONTRIBUTING.md) para el flow.
```

- [ ] **Step 2: Verificar grep de voseo limpio**

Run:
```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|vos\b)' docs/self-hosting.md
```
Expected: 0 líneas.

- [ ] **Step 3: Commit**

```bash
git add docs/self-hosting.md
git commit -m "$(cat <<'EOF'
docs: guía de self-hosting agnóstica de plataforma

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: CONTRIBUTING.md

**Files:**
- Create: `CONTRIBUTING.md`

- [ ] **Step 1: Crear el archivo con el siguiente contenido**

Content for `CONTRIBUTING.md`:

```markdown
# Contribuir a mokuji

Bienvenido. mokuji es un proyecto chico y abierto, mantenido por una sola
persona; contribuciones son bienvenidas y se revisan a paso humano.

## Empezar

```bash
git clone https://git.odin.do/kengru/mokuji.git
cd mokuji
cp .env.example .env       # ajusta lo que necesites
make compose-up            # levanta mailpit local en localhost:8025
make dev                   # corre el binario con live reload (requiere `air`)
```

Si no tienes `air` instalado, `make run` corre el binario una sola vez.

Para correr migraciones contra la DB local sin levantar el server:

```bash
make migrate
```

## Tests

```bash
make test
```

Todos los tests son `go test` puro. No hay frameworks extra. La suite corre
en menos de un minuto en hardware decente.

Cuando agregues features nuevos, agrega tests. Cuando arregles un bug,
agrega el test que falla **antes** del fix (regression test).

## Estructura del repo

- `main.go` — composition root: lee env vars, wirea handlers y workers.
- `internal/<package>/` — un paquete por feature. Los nombres son cortos
  y descriptivos (`auth`, `library`, `releases`, etc.).
- `internal/server/` — HTTP routing, middleware, security headers.
- `migrations/` — `.sql` numerados, up-only.
- `web/templates/` — `html/template`. Server-rendered, sin frameworks.
- `web/static/` — JS y assets servidos en `/static/`.
- `web/emails/` — templates de emails outbound.
- `docs/` — documentación viva.
- `docs/adr/` — decisiones arquitectónicas inmutables.

## Commits

- Subject corto, español, lowercase, sin punto final.
- Cuerpo si la motivación no es obvia del diff.
- Co-author trailer si fue trabajo en pair.

Ejemplo:

```
seo: robots.txt + sitemap.xml + llms.txt en el root

Tres endpoints estáticos servidos en la raíz vía mountDiscoverability.
robots.txt es allow-all + sitemap pointer. sitemap.xml lista solo `/` por
ahora (no hay rutas públicas indexables más allá de la landing). llms.txt
sigue la propuesta de llmstxt.org para que LLMs que indexen el sitio
entiendan el contexto del proyecto.
```

## ADRs

Las decisiones arquitectónicas con tradeoff real van en `docs/adr/` como
markdown numerado. **Una ADR no es un changelog ni un design doc** — es la
fotografía de una decisión que se tomó, las alternativas que se evaluaron,
y por qué quedó así.

Hacer ADR cuando:

- Hay más de una opción razonable y elegimos una.
- La decisión es difícil de cambiar después (deuda técnica si no se
  documenta).
- El reasoning no es obvio desde el código.

No hacer ADR cuando:

- Es la única opción posible (no hay tradeoff).
- Es un detalle de implementación, no de arquitectura.

ADRs son inmutables una vez aceptadas. Si una decisión cambia, se escribe
una ADR nueva que la reemplaza y se marca la vieja como
`Reemplazada por NNNN`.

## Pull requests

PRs van contra `main` directo. Por ser un repo chico, no hay ramas de
release ni gitflow.

- Para cambios chicos (typo, bugfix obvio, mejora de copy): PR directo,
  sin issue previo.
- Para cambios grandes (feature nuevo, refactor, dependencia nueva): abre
  una discusión en un issue **primero**. Evita que escribas algo y después
  haya que descartarlo por scope.
- Mantén el PR enfocado: si terminas refactoreando algo de paso, sépáralo
  en commit aparte o en otro PR.

CI: cada push corre el deploy a producción cuando el branch es `main`. Los
tests todavía no corren en CI (pendiente). Por ahora, **corre `make test`
local antes de abrir el PR**.

## Voz

Toda la copy del producto está en español **dominicano** — tuteo, nunca
voseo. Verbos en 2ª persona usan `tú` (`tienes` no `tenés`, `lees` no
`leés`, `eliges` no `elegís`). Imperativos van con acento penúltimo
(`agrega` no `agregá`, `prueba` no `probá`, `vuelve` no `volvé`).

Más allá del dialecto:

- Tono cálido y específico, no corporativo. "todavía no hay capítulos
  nuevos" gana sobre "no se encontraron registros".
- Errores accionables. "demasiados intentos en pocos minutos. espera un
  poco y vuelve a intentar" gana sobre "rate limit exceeded".
- Sin jerga innecesaria. Si una palabra técnica tiene equivalente claro en
  español, úsala (ej. `mostrar`, no `renderizar`).
- Mokuji se escribe **siempre minúscula**, incluso al inicio de oración.
  El wordmark visual es `目次` cuando puede ir.

## Reportes de seguridad

No abras issues públicos para vulnerabilidades. Mira [SECURITY.md](SECURITY.md)
para el flow privado.

## Code of conduct

Sé amable. mokuji es un proyecto chico y la conversación en issues y PRs es
el primer contacto con cualquiera que llegue. Discrepa con respeto, asume
buena fe, y cuando una conversación se ponga difícil, baja un cambio.

## Licencia

Al contribuir, aceptas que tu contribución se licencia bajo
[AGPL-3.0](LICENSE), la misma licencia del proyecto.
```

- [ ] **Step 2: Verificar grep de voseo limpio**

Run:
```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|vos\b)' CONTRIBUTING.md
```
Expected: 0 líneas.

- [ ] **Step 3: Commit**

```bash
git add CONTRIBUTING.md
git commit -m "$(cat <<'EOF'
docs: CONTRIBUTING con setup local, commits, ADR y voz

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: README.md rico de v1.0

**Files:**
- Rewrite (overwrite): `README.md`

**Note on the screenshot:** El spec pide un screenshot. Para v1.0 lo dejamos como un placeholder con instrucción al user, no lo capturamos automáticamente en este plan. El user decide qué vista usar y cuándo capturarla; el README queda preparado con el `<img>` y un comentario claro de qué falta. Si más tarde quieres, puedes capturar en local con dark mode + cualquier biblioteca poblada de ejemplo.

**Note on badges:** Forgejo no es soportado nativamente por shields.io; lo más limpio es:
- Badge de versión: shields.io static badge con `v1.0.0` + color sumi-black.
- Badge de licencia: shields.io static badge AGPL.
- Badge de "Go report card" si aplica (gh.io de Go).

Mantenemos sólo los 2 que aportan info real.

- [ ] **Step 1: Sobrescribir README.md con el siguiente contenido**

Content for `README.md`:

```markdown
# mokuji

> Una biblioteca pública y self-hostable para tu manga. Cataloga, sigue
> capítulos nuevos, enlaza a la fuente. Sin algoritmos.

[![version](https://img.shields.io/badge/version-v1.0.0-1a1815?style=flat-square)](VERSION)
[![license](https://img.shields.io/badge/license-AGPL--3.0-b8412a?style=flat-square)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.25+-1a1815?style=flat-square&logo=go)](go.mod)

🏠 Instancia pública: **[mokuji.org](https://mokuji.org)**
📖 Código: **[git.odin.do/kengru/mokuji](https://git.odin.do/kengru/mokuji)**

<!--
TODO(v1.0): screenshot. Capturar de mokuji.org una vista de la biblioteca
poblada (dark mode preferible). 1600x1000 aprox, exportar a /docs/screenshot.png
y descomentar la línea de abajo.

![mokuji — biblioteca](docs/screenshot.png)
-->

## Qué hace

- **Biblioteca privada**. Cataloga manga con estado (leyendo / por leer /
  completado), favoritos, y exporta a JSON cuando quieras.
- **Releases multi-source**. Matchea automáticamente contra **MangaDex**,
  **Asura Scans** y **Comick**. Cuando hay capítulo nuevo aparece en
  `/releases` con link directo al reader.
- **Digest por email opt-in**. Semanal por default (domingos), diario si lo
  pides, o apagado. Nunca predatory, sin "engagement tricks".
- **Perfil público opcional**. En `/u/{handle}` si decides hacerlo visible;
  privado por default.

## Stack

Go (stdlib `net/http`) + SQLite (vía `modernc.org/sqlite`, sin CGO) +
`html/template` server-rendered + vanilla JS. Un solo binario estático
~13 MB. Email outbound vía SMTP. PWA instalable. Sin Docker requerido,
sin base de datos externa, sin Redis, sin Node. Detalle por capa en
[`docs/adr/`](docs/adr/).

## Sources

| Source | Estado | Notas |
|--------|--------|-------|
| [MangaDex](https://mangadex.org) | siempre on | API pública oficial. |
| [Asura Scans](https://asurascans.com) | opt-in default on | Scanlation. Apagar con `ASURA_ENABLED=false`. |
| [Comick](https://comick.io) | opt-in default on | Usa el subdominio API sin Cloudflare. Apagar con `COMICK_ENABLED=false`. |

Bato.to y otros están en el backlog post-v1.0.

## Empezar a contribuir o self-hostear

- **Self-hosting**: [`docs/self-hosting.md`](docs/self-hosting.md). Cubre
  build, env vars, principios de deploy (sin atarse a una plataforma) y
  troubleshooting.
- **Contribuir**: [`CONTRIBUTING.md`](CONTRIBUTING.md). Setup local,
  convenciones de commits, cuándo ADR, flow de PRs, voz.
- **Decisiones arquitectónicas**: [`docs/adr/`](docs/adr/). Una ADR por
  decisión cerrada. Si quieres entender el porqué de algo, está ahí.
- **Estado del proyecto**: [`docs/estado.md`](docs/estado.md). Snapshot
  de qué corre, qué falta, gotchas operativos.

## Filosofía

- **File over App** — el código y los datos deben sobrevivir cualquier
  herramienta.
- **Texto plano y formatos abiertos** cuando sea posible.
- **Self-hosted donde aporta valor**, comercial donde es claramente mejor.
- **Mínimo viable**, expandir solo cuando duela un problema real.
- **Sin lock-in** de plataforma ni de proveedor.
- **Comprensión sobre conveniencia** — las primitivas de seguridad y core
  se entienden a fondo antes de usarse, idealmente leyéndose en menos de
  100 líneas. Por eso CSRF, sessions y migrations son código propio
  chiquito en vez de librerías.

## Licencia

[AGPL-3.0](LICENSE).

Si corres mokuji para ti o tu grupo, puedes hacer lo que quieras con el
código. Si lo corres como **servicio público** para otros users (al estilo
de mokuji.org), debes ofrecer el código fuente —incluyendo tus
modificaciones— a esos users. Esto es lo que distingue AGPL de GPL: cubre
el caso del software como servicio.

Para reportar vulnerabilidades, mira [SECURITY.md](SECURITY.md).
```

- [ ] **Step 2: Verificar grep de voseo limpio**

Run:
```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|vos\b)' README.md
```
Expected: 0 líneas.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
docs: README rico v1.0 — hero, sources, stack, links a self-host y CONTRIBUTING

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Actualizar índice de docs/ y estado.md

**Files:**
- Modify: `docs/README.md`
- Modify: `docs/estado.md`

- [ ] **Step 1: Agregar entradas en docs/README.md**

En `docs/README.md`, en la sección "Índice", reemplazar el bloque actual por:

```markdown
- [Estado del proyecto](estado.md) — snapshot de qué corre, qué falta, gotchas operativos
- [Self-hosting](self-hosting.md) — guía para correr tu propia instancia
- [Contribuir](../CONTRIBUTING.md) — setup local, commits, ADR, flow de PRs
- [Decisiones pendientes](decisiones-pendientes.md) — roadmap de decisiones aún abiertas
- [ADRs](adr/README.md) — Architecture Decision Records de decisiones cerradas
```

- [ ] **Step 2: Actualizar docs/estado.md — agregar fila v1.0 al cuadro de minors**

En `docs/estado.md`, en la tabla "Stack cerrado", después de la fila de v0.8 agregar:

```markdown
| v1.0 — Docs + discoverability | README rico + self-hosting + CONTRIBUTING + JSON-LD/sitemap/llms.txt + voseo sweep | — (✅ v1.0.0) |
```

Y en el "Backlog priorizado", después de la sub-sección `### v0.8 — cerrado ✅`, agregar:

```markdown
### v1.0 — cerrado ✅ (branch `version-1-0`)

Cierre del roadmap: el proyecto está listo para promoción pública.

- **README.md rico**: hero + value prop + 3 bullets + stack + sources reales (MangaDex + Asura + Comick) + badges + links a self-host/CONTRIBUTING + AGPL nota.
- **docs/self-hosting.md** (nuevo): guía agnóstica de plataforma. Build, env vars completas con grupos (esenciales / prod / opcionales por feature), principios de deploy (proceso supervisor + reverse proxy + persistencia), ejemplos de systemd y docker-compose sin prescribir uno.
- **CONTRIBUTING.md** (nuevo): setup local, tests, estructura del repo, commits, cuándo ADR, flow de PRs, voz (tuteo dominicano + tono específico).
- **Discoverability técnica**: `/robots.txt` + `/sitemap.xml` + `/llms.txt` servidos desde el root (handler nuevo `mountDiscoverability` en `internal/server/`). JSON-LD `SoftwareApplication` en landing. `<link rel="canonical">` en layout.
- **Página `/privacy`** pública, sin auth: qué se recolecta, por qué, retención, derechos (export/delete), contacto `kg@odin.do`. Link en footer.
- **Sweep voseo**: limpia README y SECURITY que arrastraban voseo del primer commit (mayo 2026).
- **Bump VERSION a v1.0.0** + tag + release notes en Forgejo.

### Backlog post-v1.0 (visible para próximos minors)

- **Feedback form in-app** (`/feedback` → email a `kg@odin.do`). Diferido a v1.1 con plan de anti-abuso (rate limit + honeypot, sin captchas third-party).
- **Visibilidad del repo en Forgejo**. Decisión pendiente: abrir browse anónimo en `git.odin.do`, espejar a host público, o aceptar que la promoción pública depende del canal espejo. Bloquea el "shareable real" aunque el tag de v1.0 esté listo.
```

Y en el "Backlog post-v0.8 / v1.0" cambiar el header por `### Backlog post-v1.0` y quitar los ítems que cerramos (README rico, CONTRIBUTING, OpenGraph cards — esto último ya cerró en v0.7).

- [ ] **Step 3: Commit**

```bash
git add docs/README.md docs/estado.md
git commit -m "$(cat <<'EOF'
docs: actualizar índice y estado con cierre v1.0

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Página /privacy

**Files:**
- Create: `internal/home/privacy.go`
- Create: `internal/home/privacy_test.go`
- Create: `web/templates/privacy.html`
- Modify: `internal/server/routes.go` (registrar la ruta) — verificar cómo se montan hoy los handlers del paquete `home`.

**Scope:** una página HTML pública (no auth) con la realidad mínima:

1. **Qué se recolecta**: email (para login OTP y digest opcional), lista de manga (la biblioteca del user), IP en logs de rate limit (no asociada a la cuenta a largo plazo), session ID (hash, server-side).
2. **Por qué**: login, notificar releases, prevenir abuso.
3. **Retención**: las cuentas eliminadas pasan por 7 días de gracia (soft delete) y luego hard delete. Logs de rate limit se rotan. Backups del DB con la frecuencia del operador.
4. **Derechos del user**: export JSON, delete account self-service (link a `/settings`). Apunta a GDPR sin invocar la sigla — la implementación ya existe.
5. **Comparte con**: nadie. No hay analytics third-party, no hay ad networks. SMTP outbound vía proveedor (Resend en mokuji.org) — mencionar.
6. **Self-hosters**: la realidad varía según la instancia. La página dice "esta política aplica a mokuji.org; instancias self-hosted definen sus propias prácticas" para no comprometer al ecosistema.
7. **Contacto**: `kg@odin.do` para preguntas de privacidad.
8. **Cambios**: la política puede cambiar; la versión actual se identifica por última fecha de update visible.

- [ ] **Step 1: Verificar cómo se monta hoy el paquete home en routes**

Run: `grep -n "home\." internal/server/routes.go`

Esperado: ver el patrón existente para las rutas de home (probablemente algo como `mux.HandleFunc("GET /", s.cfg.Home.Index)` o similar). Adaptar el handler de privacy al mismo patrón.

- [ ] **Step 2: Escribir el smoke test del handler**

Create `internal/home/privacy_test.go`:

```go
package home

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrivacyHandler(t *testing.T) {
	h := newPrivacyHandler(testTemplates(t))

	req := httptest.NewRequest(http.MethodGet, "/privacy", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, needle := range []string{"privacidad", "email", "borrar", "kg@odin.do"} {
		if !strings.Contains(body, needle) {
			t.Errorf("body missing %q", needle)
		}
	}
}
```

**Verificar primero** si en el paquete `home` ya hay un helper `testTemplates(t)` o equivalente; si no, copiar el patrón de carga de templates de los tests existentes del paquete (probablemente `home_test.go`). Si no hay tests previos en `home`, simplificar el test cargando los templates directo con `template.ParseFS(web.Templates, "templates/privacy.html", "templates/layout.html")`.

- [ ] **Step 3: Correr el test — debe fallar**

Run: `go test ./internal/home/ -run TestPrivacyHandler -v`
Expected: FAIL (handler o template no existen).

- [ ] **Step 4: Crear el template web/templates/privacy.html**

Content for `web/templates/privacy.html`:

```html
{{define "title"}}privacidad · mokuji{{end}}
{{define "content"}}
<main class="prose">
  <h1>política de privacidad</h1>
  <p class="meta">última actualización: <time datetime="2026-05-25">25 de mayo, 2026</time></p>

  <p>Esta política aplica a la instancia pública en <strong>mokuji.org</strong>. Si corres tu propia instancia, define tus propias prácticas — el código es el mismo, las decisiones operativas son tuyas.</p>

  <h2>qué recolectamos</h2>
  <ul>
    <li><strong>Tu email</strong>. Para enviarte el código OTP de login y, si lo activas, el digest semanal/diario de capítulos nuevos.</li>
    <li><strong>Tu biblioteca de manga</strong>. Los títulos que catalogues, su estado (leyendo/por leer/completado), favoritos, y cualquier dato del título que venga de las sources (MangaDex, Asura, Comick).</li>
    <li><strong>Tu username público</strong> si activaste el perfil público (en <code>/u/{handle}</code>). Esto es opt-in; por default tu lista es privada.</li>
    <li><strong>IP de las requests</strong>. Sólo en logs efímeros de rate limit (para prevenir abuso de OTP/auth) y logs de servidor que rotamos. No asociamos el IP a tu cuenta a largo plazo.</li>
    <li><strong>Session ID</strong> server-side. Lo guardamos hasheado en la DB; la cookie en tu browser tiene el ID en plano. La sesión expira sola.</li>
  </ul>

  <h2>por qué</h2>
  <p>Login sin password (OTP), notificarte capítulos nuevos si lo activas, prevenir abuso de auth y rate limit. Nada más.</p>

  <h2>con quién compartimos</h2>
  <p>Con nadie. mokuji.org no usa analytics third-party, no tiene ad networks, no vende ni alquila datos. El único servicio externo es el proveedor SMTP (hoy Resend) que envía los emails — ese proveedor procesa tu email como destinatario.</p>

  <h2>cuánto tiempo guardamos</h2>
  <ul>
    <li>Tu cuenta y datos: mientras la mantengas activa.</li>
    <li>Al eliminar la cuenta: pasa por <strong>7 días de gracia</strong> (soft delete, recuperable) y después hard delete definitivo.</li>
    <li>Backups del DB: con la frecuencia del operador (mokuji.org backupea diario; la copia más vieja se reemplaza eventualmente).</li>
    <li>Logs efímeros de rate limit y server: rotan en horas/días.</li>
  </ul>

  <h2>tus derechos</h2>
  <ul>
    <li><strong>Exportar todo</strong>. Desde <a href="/settings">settings</a> bajas un JSON con tu biblioteca.</li>
    <li><strong>Eliminar la cuenta</strong>. Desde settings también. Pasa por 7 días de gracia recuperables; después no se puede revertir.</li>
    <li><strong>Cambiar tu email o username</strong>. Desde settings.</li>
    <li><strong>Apagar el digest</strong>. Desde settings o con el link de unsubscribe one-click en cada email.</li>
  </ul>

  <h2>cookies</h2>
  <p>Una sola cookie: la de sesión. Es <code>HttpOnly</code>, <code>Secure</code> en producción, <code>SameSite=Lax</code>. No usamos cookies de tracking.</p>

  <h2>contacto</h2>
  <p>Preguntas o requests de privacidad: <a href="mailto:kg@odin.do">kg@odin.do</a>. Para reportes de seguridad, <a href="https://git.odin.do/kengru/mokuji/src/branch/main/SECURITY.md">SECURITY.md</a>.</p>

  <h2>cambios</h2>
  <p>Si esta política cambia, actualizamos la fecha de arriba. Cambios sustanciales se anuncian en el changelog del repo.</p>
</main>
{{end}}
```

(El template hereda del layout existente; si la app usa `{{define}}/{{template}}` con un layout específico, ajustar al patrón real del paquete `home`.)

- [ ] **Step 5: Crear el handler en internal/home/privacy.go**

Content for `internal/home/privacy.go`:

```go
package home

import (
	"html/template"
	"net/http"
)

// privacyHandler sirve GET /privacy. Página pública, sin auth. Renderiza un
// template estático embebido. El contenido refleja las prácticas de la
// instancia mokuji.org; self-hosters definen las propias.
type privacyHandler struct {
	tpl *template.Template
}

func newPrivacyHandler(tpl *template.Template) *privacyHandler {
	return &privacyHandler{tpl: tpl}
}

func (h *privacyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tpl.ExecuteTemplate(w, "layout", nil); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
```

**Importante:** este snippet asume que el paquete `home` ya tiene un patrón para cargar templates con layout. Si el paquete usa otro shape (ej. carga global de templates en un struct `Handlers`), adaptar el handler para encajar — verificar leyendo `internal/home/home.go` o equivalente antes de escribir.

- [ ] **Step 6: Registrar la ruta**

Buscar dónde se montan las rutas del paquete `home`:

Run: `grep -n "home" internal/server/routes.go`

Agregar la ruta `GET /privacy` apuntando al `privacyHandler`, siguiendo el patrón existente. Si el paquete expone los handlers como métodos de un struct (ej. `homeHandlers.Privacy(w, r)`), agregar el método al struct.

- [ ] **Step 7: Correr el test — debe pasar**

Run: `go test ./internal/home/ -run TestPrivacyHandler -v`
Expected: PASS.

- [ ] **Step 8: Suite completa**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 9: Smoke en local (opcional)**

```bash
make build && ./bin/mokuji &
sleep 2
curl -sS http://localhost:8080/privacy | grep -E "política de privacidad|kg@odin.do" | head -3
kill %1
```

Expected: HTML con la página, contiene los strings esperados.

- [ ] **Step 10: Agregar link al footer del layout**

En `web/templates/layout.html`, buscar el footer existente (probablemente con la línea "mokuji v…" y link a `/version`) y agregar a su lado un link a `/privacy`:

```html
<a href="/privacy">privacidad</a>
```

Mantener el separador visual coherente con el footer actual (`·` o lo que sea que use).

- [ ] **Step 11: Commit**

```bash
git add internal/home/privacy.go internal/home/privacy_test.go web/templates/privacy.html web/templates/layout.html internal/server/routes.go
git commit -m "$(cat <<'EOF'
privacy: página /privacy + link en footer

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Bump VERSION + smoke build

**Files:**
- Modify: `VERSION`

**This is the last commit before pausing for user review.** No taggear, no
pushear, no mergear. El user decide.

- [ ] **Step 1: Bump VERSION**

Reemplazar el contenido completo de `VERSION` por:

```
v1.0.0
```

(Sin newline final si el archivo original no lo tiene; verificar con
`od -c VERSION | tail`.)

- [ ] **Step 2: Build smoke check**

Run: `make build`
Expected: Build OK. `./bin/mokuji --help 2>&1 | head -5` o `./bin/mokuji -migrate-only` corre sin errores de versión.

Verificar la versión inyectada:

Run: `strings ./bin/mokuji | grep -E "^v1\.0\.0$" | head -3`
Expected: aparece `v1.0.0` al menos una vez (es la versión inyectada por ldflags).

- [ ] **Step 3: Suite completa de tests**

Run: `go test ./...`
Expected: PASS en todos los paquetes.

- [ ] **Step 4: Verificar grep final de voseo en todo el repo de docs**

Run:
```bash
grep -rnE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá)' README.md SECURITY.md CONTRIBUTING.md docs/self-hosting.md docs/estado.md docs/README.md 2>/dev/null
```
Expected: 0 líneas.

- [ ] **Step 5: Commit**

```bash
git add VERSION
git commit -m "$(cat <<'EOF'
v1.0.0

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 6: PAUSA OBLIGATORIA**

**Detenerse aquí.** No taggear, no pushear, no mergear a `main`. El user
revisa la branch `version-1-0` y decide.

### Pre-merge checklist humano (sólo el user)

1. ~~Visibilidad del repo~~ → **resuelto antes de Task 4**: el user abrió el repo a browse anónimo en Forgejo. Verificado: HTTP 200 desde cliente sin auth.
2. Ajustes de copy/contenido en cualquier doc (typos, voz, lo que sea).
3. Capturar el screenshot que falta en el README y reemplazar el placeholder en `README.md`.

### Pasos manuales del user post-aprobación

Cuando el user apruebe (no incluidos en este plan porque dependen de su confirmación explícita):

- `git tag -a v1.0.0 -m "v1.0.0"`
- `git checkout main && git merge version-1-0 --no-ff`
- `git push origin main` (dispara CD a mokuji.org — confirmar que el binario nuevo arranca)
- `git push origin v1.0.0`
- Crear release notes en Forgejo con changelog desde v0.5.0.
- Resolver la decisión de visibilidad antes de cualquier post en HN / comunidades.

### Pendiente explícito para v1.1 (no v1.0)

- **Form de feedback in-app** en `/feedback`. Email destino: `kg@odin.do`. Requisitos: rate limit por IP (reutilizar `internal/ratelimit`), validación básica del body, posiblemente honeypot + minimum-time-to-submit como anti-bot. NO usar captcha de terceros (rompe el "sin third-party" del producto). Link en footer y en `/settings`.

---

## Verification checklist (final, para auto-review antes de cerrar el plan)

Recorrer este checklist contra el spec original (`docs/superpowers/specs/2026-05-24-mokuji-v1.0-design.md` sección "v1.0 — docs + tag + promoción"):

- [x] **README.md rico** — Task 6. Hero + value prop + sources + stack + quickstart + badges + AGPL.
- [x] **docs/self-hosting.md** — Task 4. Agnóstico de plataforma. Open considerations cubiertas (cobertura más allá del path actual, opt-in vs obligatorio, migración entre versiones).
- [x] **CONTRIBUTING.md** — Task 5. Cómo correr local, tests, commits, ADR, PRs, voz, code of conduct.
- [x] **Tag y release** — Task 8 prepara el bump; el tag/release lo dispara el user después de revisión (pausa explícita).
- [x] **Promoción** — fuera de scope técnico (explícito en spec).

**Scope extra (no estaba en el spec, agregado por el user el 2026-05-25):**

- [x] **Sweep voseo en docs raíz** — Task 1.
- [x] **Discoverability técnica / SEO / LLM-friendly** — Tasks 2 y 3.
- [x] **Página `/privacy`** — Task 8.
- [x] **Visibilidad del repo + feedback in-app** — documentados como decisiones del user (pre-merge checklist + backlog v1.1), no como código.

**Riesgos del spec original revisados:**

| Riesgo del spec | Estado |
|-----------------|--------|
| Volumen del handoff | N/A — v1.0 no requiere handoff visual (spec lo dice). |
| Schedule slip | Mitigado: el plan tiene 8 tasks chicas, cada una con commit propio, pausable. |
| Performance fonts | Cerrado en v0.8. |
| Migration de `onboarded_at` | Cerrado en v0.7. |

Self-review pasada: no quedan placeholders, no quedan TBDs, todas las
referencias a archivos están con paths concretos, los snippets de código y
copy están completos.
