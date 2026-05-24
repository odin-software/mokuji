# Diseño: identidad visual "01 × 02-type"

> **Status**: implementada. Phases 1–4 mergeadas en `branding`. Resuelve el ítem "Marca / identidad visual" de [`decisiones-pendientes.md`](decisiones-pendientes.md).

## Resumen

Refresh completo de identidad para mokuji.org. Estética editorial (numbered table of contents, vermillón sobre crema, sin esquinas redondeadas) tipografiada en Albert Sans + Noto Serif JP + IBM Plex Mono. El kanji 目次 (mokuji = "tabla de contenidos") es la base del wordmark.

La fuente de verdad visual vive fuera del repo, en el handoff de design (zip enviado por separado):

- `design_handoff_mokuji_identity/README.md` — guía de aplicación, tokens, iconografía.
- `design_handoff_mokuji_identity/design_canvas/Mokuji Identity.html` — canvas completo (mobile, desktop, emails, marketing, errores).

## Fases aplicadas

| Fase | Alcance | Estado |
|------|---------|--------|
| 1 | Foundation: design tokens en `<style>` de `layout.html`, fuentes Google, icono 目 + maskable, manifest | ✅ |
| 2 | `auth/start`, `auth/verify`, `library/list`, `releases` (eyebrow + page-head + accent, OTP grande monospace) | ✅ |
| 3 | `home`, `settings`, `profile`, `library/entry`, `library/search`, `delete-account`, `restore-account`, `unsubscribe` | ✅ |
| 4 | `404.html`, `500.html`, emails HTML (`digest.html`, `otp.html`, `account-deletion.html`) | ✅ |

Todos los design tokens (paleta, tipografía, espaciado, shape) están como CSS custom properties en `:root` de `layout.html`, con override en `@media (prefers-color-scheme: dark)`. No hay rounded corners en ninguna superficie.

## Cambios de backend para soportar las plantillas

El handoff prometía "no backend changes needed" pero algunas plantillas referenciaban campos nuevos que requerían wiring mínimo. Documentamos acá lo que se tocó para no perder el porqué.

### `homeData.Username` (`internal/home/home.go`)

La plantilla `home.html` saluda con `{{if .Username}}@{{.Username}}{{else}}{{.Email}}{{end}}`. Antes el handler solo pasaba `Email`; ahora también pasa `Username` (= `slug.String` cuando hay slug). Es el mismo dato ya cargado en el SELECT — sin query nueva.

### `profileData.Counters` (`internal/profile/profile.go`)

`profile.html` renderiza un strip de tres contadores (leyendo / por leer / completado) si `{{if .Counters}}` no es nil. El campo se popula con `countByStatus(entries)`, función pura que recorre las entries ya cargadas e incrementa los buckets. **No agregamos query SQL** — el N de entries por usuario es chico y la query principal ya las trae todas. Si en algún momento paginamos `loadEntries`, va a hacer falta una `SELECT status, COUNT(*) GROUP BY` separada.

### 404 / 500 estilizados (`internal/server/errors.go`)

Nuevo `errorRenderer` con dos templates (`layout.html + 404.html`, `layout.html + 500.html`). Wiring:

- **404**: catch-all `mux.HandleFunc("/", s.errs.NotFound)` en `routes.go`. Los patterns con verbo (`GET /...`) y los paths exactos siguen ganando por longest-match, así que solo dispara en paths desconocidos.
- **500**: integrado en el `recoverer` middleware. Pasa el `request_id` como `ErrorID` (visible en la página, para que el usuario pueda copiarlo a un issue) y `time.Now().UTC().Format(time.RFC3339)` como `RequestedAt`.

Si `HTMLTemplates` no se pasa al `server.Config` (caso edge: tests viejos), el renderer queda nil y se cae al `http.Error` plano de antes — el servidor no se rompe.

**Cobertura intencional limitada**: solo se enchufa al recoverer. Los muchos `http.Error(w, "internal error", 500)` que hay en handlers individuales siguen siendo texto plano. Cambiar todos los call sites es ruido para esta iteración; si en el futuro queremos página estilizada en todos los 500, conviene exponer `s.errs.ServerError` por interfaz o pasarlo por DI en lugar de hacer un find-and-replace.

### Emails multipart (opt-in HTML)

Tanto `internal/auth/auth.go` (OTP) como `internal/profile/delete.go` (account-deletion) ahora renderean `.html` además de `.txt` y mandan `multipart/alternative` vía `email.SendMessage`. El soporte HTML es **opt-in por presencia de archivo**: si `otp.html` / `account-deletion.html` no existen en `EmailTemplates` (deploys viejos o tests con FS sintético), `fs.Stat` falla y el handler manda solo texto. Cero cambios de wiring SMTP — `email.SendMessage` ya soportaba HTMLBody (lo usaba el digest).

El digest semanal (`releases/digest`) no cambió en su wiring: ya enviaba HTML; solo se actualizó el template para que use la paleta nueva.

## Lo que quedó fuera

- **Layout desktop con sidebar** (el canvas muestra masthead + left rail a 1440px). El `body` sigue capeado a `max-width: 42rem`. Cuando se quiera, es cambio CSS-only en `layout.html`.
- **OpenGraph meta tags + cards 1200×630**. Documentado en el canvas, no implementado.
- **Onboarding 3-step swipeable** para nuevos usuarios. Tampoco implementado.
- **Email previews en /admin**. Hoy se prueban end-to-end mandando OTPs reales a Mailpit en dev (compose).

## Verificación local

```sh
make dev
# /auth          → wordmark + eyebrow + "entrar a mokuji" con vermillón
# /auth/verify   → OTP grande monospace con tracking ancho
# /library       → eyebrow "Index · N entries", badges 01/02/03, favorito ●
# /releases      → eyebrow con count + accent en "capítulos"
# /this-no-existe → 404 con kanji 迷
# Activar dark mode del SO → paleta cream sobre sumi-black
```
