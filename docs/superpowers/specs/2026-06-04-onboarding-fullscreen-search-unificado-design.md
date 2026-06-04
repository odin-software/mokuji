# Onboarding full-screen + búsqueda unificada — design

**Fecha**: 2026-06-04
**Autor**: Kendry + Claude (brainstorm)
**Versión base en producción**: v1.0.4
**Scope**: corrido (no atado a un minor específico; se tagea cuando Kendry corte)

## Objetivo

Dos mejoras de UX sobre la app ya lanzada (v1.0.x), ambas de **pulido /
arquitectura de información**, no de features nuevas:

1. **Onboarding full-screen** — el wizard de primer uso debe ser un takeover
   de pantalla completa (sin chrome de app) en mobile y desktop, con botón
   de skip. Como el onboarding de cualquier app moderna.
2. **Búsqueda unificada** — una sola caja de búsqueda en `/library` que
   matchea tu biblioteca **y** ofrece agregar desde el catálogo (AniList),
   en lugar de las dos búsquedas separadas de hoy.

### Motivación

- **Onboarding**: el wizard de 3 pasos existe (v0.7) pero es una columna
  mobile de 26rem sin `@media`. En desktop queda chico y centrado con todo
  el real estate vacío, y peor: muestra el sidebar/nav de la app de fondo,
  cuando debería ser un momento de foco total. No hay forma de saltarlo.
- **Búsqueda**: hoy hay **dos** búsquedas con scopes distintos —
  `buscar en tu lista` en `/library` (filtra lo que ya tienes) y
  `buscar por título` en `/library/search` (busca AniList para agregar).
  Un usuario espera **una** búsqueda. Validado en campo: un amigo usando la
  app no entendió que la caja de `/library` filtraba su biblioteca cuando él
  quería buscar **uno nuevo para agregar**. Además, como uno vive en
  `/library`, agregar obliga a un context switch a otra página.

## Item 1 — Onboarding full-screen

### Comportamiento

- El wizard (`/onboarding`, `/onboarding/step2`, `/onboarding/step3`) ocupa
  todo el viewport: **sin sidebar (desktop), sin app-nav (top mobile), sin
  footer**. Solo el contenido del onboarding.
- Un control **skip / saltar** visible (secundario, no compite con el CTA
  primario) que completa el onboarding y va a `/library`.
- Pulido visual para que la composición se vea intencional a lo ancho (el
  kanji/visual respira en desktop, llena en mobile). **No** se repiensan los
  pasos ni el contenido — sigue siendo el wizard informativo de 3 pasos.

### Mecanismo (decisión cerrada): block override

El chrome hoy está gateado por `{{if .Session}}` en `layout.html`. Durante
onboarding el user está autenticado, así que el chrome se renderiza. No
existe un modo "autenticado pero sin chrome" (solo "sin sesión = sin chrome",
que usan landing y `/auth`).

Se introduce ese modo con bloques overrideables de `html/template`:

- `layout.html` envuelve el chrome en `{{block "chrome" .}}…default…{{end}}`
  (sidebar + `app-nav`) y el footer en `{{block "appfooter" .}}…{{end}}`.
- El contenido por defecto de esos bloques es el chrome actual, **idéntico**
  → todas las páginas existentes no cambian (heredan el default).
- Los templates de onboarding (`step1/2/3.html`) definen
  `{{define "chrome"}}{{end}}` y `{{define "appfooter"}}{{end}}` (vacíos) →
  sin chrome, **sin markup muerto** en el DOM.

El full-bleed se reusa del camino existente: sin `.app-sidebar` renderizado,
la regla `.app-shell:not(:has(> .app-sidebar))` ya degrada a layout de bloque
(la que usan landing/auth). En mobile, con `app-nav` y footer fuera, queda
pantalla completa. Se agrega CSS mínimo en el contenedor de onboarding
(`min-height: 100dvh`, centrado) para que llene el viewport con intención.

### Skip (casi gratis)

El handler `finish` (`POST /onboarding/finish`) ya defaultea
`digest_frequency` a `weekly` cuando el campo no viene. El skip es entonces
un `<form method="POST" action="/onboarding/finish">` con su `csrfInput` y
sin radios → setea `onboarded_at`, deja digest en `weekly`, redirige a
`/library`. **Cero cambio de handler.**

### Cambios técnicos

```
web/templates/layout.html          → envolver chrome y footer en
                                      {{block "chrome"}} / {{block "appfooter"}}
web/templates/onboarding/step1.html → {{define "chrome"}}{{}} +
web/templates/onboarding/step2.html   {{define "appfooter"}}{{}} + skip form +
web/templates/onboarding/step3.html   CSS full-viewport
```

`internal/onboarding/onboarding.go` **no cambia** (sigue parseando
`layout.html` + step; los `{{define}}` de los steps overridean los bloques).

### Verificación

- En desktop ≥1024px, `/onboarding` no muestra sidebar; ocupa el viewport.
- En mobile, no se ve `app-nav` ni footer; pantalla completa.
- Las demás páginas (`/library`, `/releases`, `/settings`, landing, `/auth`)
  renderizan el chrome **idéntico** a antes (el default block).
- Skip desde step1/step2 completa onboarding (`onboarded_at` seteado,
  `digest_frequency = weekly`) y cae en `/library`.
- Un user ya onboardeado que navega a `/onboarding` sigue redirigido a
  `/library` (comportamiento de `requireUnonboarded`, sin cambio).

## Item 2 — Búsqueda unificada en `/library`

### Comportamiento

- **Una sola caja** en `/library`, donde el user ya vive. Reemplaza el
  `buscar en tu lista` actual y absorbe el `+ agregar manga`.
- El copy señala **ambos scopes** (el placeholder list-only fue justo lo que
  confundió al amigo): placeholder tipo `buscar tu biblioteca o agregar nuevo`.
- `GET /library?q=foo` renderiza dos secciones con headers claros:
  - **en tu biblioteca** — tus entries que matchean `foo` (local, rápido).
  - **agregar nuevo** — resultados de AniList para `foo`, cada uno con
    `+ agregar`. Los que **ya tienes** salen marcados "ya en tu biblioteca"
    (dedup), no re-agregables.
- Agregar es **in-place**: `POST /library/add` redirige de vuelta a
  `/library?q=foo` (preserva el query) en vez de a `/library` pelado. El user
  se queda en contexto, ve el manga recién agregado moverse a la sección "en
  tu biblioteca" (y desaparecer/marcarse en "agregar nuevo"), y puede agregar
  varios de una misma búsqueda.
- El filtro de estado (leyendo / por leer / completado) sigue como browse de
  la lista; aplica a la sección "en tu biblioteca", no al catálogo.
- `q` vacío → comportamiento actual de la lista (sin llamada a AniList).
- `/library/search` se retira: la ruta queda como **redirect** a
  `/library?q={q}` (compat con bookmarks/links).

### Mecanismo (decisión cerrada): server-side, todo junto

`GET /library?q=foo` hace ambas cosas en un render: filtra entries locales y
—cuando `q != ""`— llama a `h.src.Search` (AniList) para la sección de
catálogo. Sin JS nuevo, 100% server-rendered (el ethos del repo).

- **Latencia**: se acepta el ~0.5s de AniList en cada búsqueda de texto. Para
  una app personal de bajo tráfico es despreciable, y la búsqueda es
  deliberada (submit con Enter, no por tecla).
- **Errores de AniList no rompen la lista**: si la búsqueda de catálogo
  falla, se renderizan los resultados de biblioteca + una nota suave ("no
  pudimos buscar en el catálogo, prueba de nuevo"), igual que el manejo de
  error del handler `search` actual.
- **Dedup**: se carga el set de `(source, source_id)` de la biblioteca del
  user; cada resultado de AniList que matchee se marca "ya en tu biblioteca"
  y se deshabilita su botón de agregar.

### Cambios técnicos

```
internal/library/library.go   → handler `list`: cuando q != "", además de
                                 filtrar entries, llama AniList y arma la
                                 sección de catálogo con dedup. listData crece
                                 (CatalogResults + flags "ya en biblioteca").
                                 handler `add`: redirect a /library?q={q}.
                                 ruta /library/search → redirect a /library?q=.
web/templates/library/list.html   → caja unificada (nuevo copy) + sección
                                     "agregar nuevo" (portada de search.html)
                                     con estado dedup.
web/templates/library/search.html → se retira (su markup de cards migra a la
                                     sección de catálogo de list.html).
```

El matching multi-source al agregar (`tryMatchSources`) **no cambia**: sigue
disparándose en `add` igual que hoy.

### Verificación

- `/library?q=foo` muestra: tus entries que matchean foo + cards de AniList
  para agregar, con los ya-en-biblioteca marcados y no re-agregables.
- Agregar un resultado redirige a `/library?q=foo` (no a `/library` pelado);
  el manga aparece en "en tu biblioteca" y deja de ofrecerse en "agregar
  nuevo".
- `/library/search` (y `/library/search?q=x`) redirige a `/library` (`?q=x`).
- Si AniList falla, la sección de biblioteca sigue renderizando + nota de
  error; no hay 500.
- `q` vacío no llama a AniList (sin latencia en el browse normal).

## Decisiones cerradas en este brainstorm

- Onboarding = **solo pulido** (full-screen + skip), no se repiensan los pasos.
- Mecanismo de chrome: **block override** (no CSS-hide, no render anónimo).
- Full-screen aplica a **mobile y desktop**.
- Búsqueda: **un solo search unificado** (no desambiguar dos cajas).
- La caja vive en **`/library`** (donde el user ya está); agregar es in-place.
- Carga de catálogo: **server-side, ambas secciones juntas** (no lazy JS).
- El copy de la caja debe señalar **ambos scopes** (lección del caso real).

## Fuera de scope

- **Typeahead / instant search** (búsqueda en vivo por tecla con JS). La
  unificada submitea con Enter; el lazy-load JS del catálogo queda como
  upgrade futuro si la latencia molesta.
- **Repensar el flujo de onboarding** (ej. empujar a "agrega tu primer
  manga" en vez de las 3 pantallas informativas). Explícitamente diferido.
- **Search global multi-página** (releases, perfiles, etc.). El scope es la
  biblioteca + catálogo de agregado, nada más.
- **Caché de resultados de AniList**. Si el rate limit molesta, se evalúa
  después.

## Riesgos y mitigaciones

| Riesgo | Prob. | Impacto | Mitigación |
|--------|-------|---------|------------|
| `{{block "chrome"}}` rompe alguna página que dependía del markup exacto | baja | medio | El default block es copy-paste idéntico del chrome actual; verificación visual de las páginas core. |
| AniList lento/caído degrada `/library` con `q` | media | bajo | Catálogo es best-effort: error no rompe la sección de biblioteca, nota suave. `q` vacío nunca llama AniList. |
| Latencia ~0.5s en cada filtro de texto molesta | baja | bajo | Aceptado para tráfico personal; upgrade a lazy-JS documentado como fuera de scope. |
| Dedup `(source, source_id)` no matchea (formatos distintos) | baja | bajo | source_id se persiste tal cual viene de AniList; el match es exacto contra lo que `add` guardó. Test de dedup. |

## Próximos pasos

1. Kendry revisa este spec.
2. Si aprueba, invocar `superpowers:writing-plans` para el plan de
   implementación ejecutable (TDD por item).
