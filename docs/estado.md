# Estado actual de mokuji

Snapshot del proyecto pensado para retomar después de tiempo (o de un context compact).
Para detalle de decisiones cerradas, ver [`adr/`](adr/). Para lo que falta decidir/implementar, ver [`decisiones-pendientes.md`](decisiones-pendientes.md).

## En una línea

Tracker de manga público en Go + SQLite. **Versión actual: v1.2.0** (ver `VERSION`; confirmar qué corre en prod con `curl https://mokuji.org/version`). Proyecto público desde v1.0, con identidad visual completa, landing, onboarding, digest semanal/diario, import de MyAnimeList y una pasada de UX + accesibilidad WCAG 2.2 AA (v1.2.0).

Flow end-to-end: signup con OTP → biblioteca con search de AniList (orden: leyendo > por leer > completados, favorito tie-break) → matching automático multi-source (MangaDex + Asura + Comick) → caps nuevos en `/releases` + email digest semanal (worker domingo 9-22 UTC con retry diferido) → onboarding (digest + visibilidad de la biblioteca) → privacy/digest toggle → export JSON → soft delete con gracia 7 días → username personalizable + perfil público `/u/{slug}`.

## Stack cerrado

| Capa | Elección | ADR |
|------|----------|-----|
| Lenguaje | Go (baseline `go 1.25`) | — |
| HTTP | stdlib `net/http` puro | [0003](adr/0003-http-framework-stdlib.md) |
| Frontend | `html/template` server-rendered + vanilla JS | [0002](adr/0002-frontend-server-rendered.md) |
| Base de datos | SQLite via `modernc.org/sqlite` (sin CGO) | [0010](adr/0010-driver-sqlite.md) |
| Distribución | Multi-tenant SaaS + open source self-hostable | [0001](adr/0001-modelo-multitenant-open-source.md) |
| Auth | Email OTP, sin passwords | [0004](adr/0004-auth-email-otp.md) |
| Sessions | Server-side en SQLite, ~100 LOC propias | [0005](adr/0005-sessions-propias.md) |
| CSRF | Synchronizer token propio, ~50 LOC | [0006](adr/0006-csrf-propio.md) |
| Email | SMTP estándar, Resend en producción | [0007](adr/0007-email-transport.md) |
| Estructura repo | Intermedia con `internal/` por feature | [0008](adr/0008-estructura-del-repo.md) |
| Migrations | Runner propio, ~60 LOC, embebido | [0009](adr/0009-migration-tooling.md) |
| Licencia | AGPL-3.0 | [0011](adr/0011-licencia-agpl.md) |
| CI/CD | GitHub Actions + deploy por SSH con llave restringida | [0015](adr/0015-cd-github-actions.md) (reemplaza a [0012](adr/0012-cicd-forgejo-runner.md)) |
| Fase 2 — Release tracking | MangaDex worker + cursor + email digest + GDPR export/delete | [0013](adr/0013-fase-2-release-tracking.md) (✅ v0.2.0) |
| Fase 3 — Multi-source | Tabla `manga_sources` + `releasesource.Client` + Asura + Comick + backfill | [0014](adr/0014-fase-3-multi-source.md) (✅ v0.3.0) |
| Fase 4 — Resiliencia / cuentas | Usernames `/u/{slug}`, flash messages, resiliencia digest (retry + recovery), separación `SMTP_FROM_PRODUCT`, rate limit `/unsubscribe` | — (✅ v0.4.0) |
| Fase 5 — Identidad visual | Rebrand "01 × 02-type" (paleta vermillón + cream + sumi-black, Albert Sans + Noto Serif JP + IBM Plex Mono, kanji 目次 wordmark), emails HTML multipart, 404/500 estilizados | [diseno-identidad](diseno-identidad.md) (✅ v0.5.0) |
| v0.6 — Landing pública | Template responsive (mobile + desktop 1024px) + default OG/Twitter meta + OG card PNG 1200×630 | — (✅ v0.6.0) |
| v0.7 — Cara externa completa | Onboarding wizard 3 pasos + auth redesign OTP + splash PWA + 5 variantes OG + desktop sidebar a 1024px + daily digest opt-in backend | — (✅ v0.7.0) |
| v0.8 — Pulido | Empty states normalizados (6 kanji) + microcopy review + email templates redesigned (digest/otp/account-deletion) + favicons PNG completos + preload Google Fonts + dark mode parity audit + errrender package para 5xx estilizados | — (✅ v0.8.0) |
| v1.0 — Docs + discoverability | README rico + self-hosting.md + CONTRIBUTING.md + JSON-LD/sitemap/llms.txt/canonical + /privacy + voseo sweep | — (✅ v1.0.0) |

## Mapa de paquetes

```
internal/
├── admin/         # /admin/trigger-digest + /admin/sources-health (Bearer auth)
├── asura/         # cliente releasesource.Client contra api.asurascans.com
├── auth/          # /auth, /auth/start, /auth/verify, /auth/logout
├── csrf/          # middleware + helper de template
├── email/         # cliente SMTP (plain Send + multipart SendMessage)
├── home/          # GET / (redirect a /library si autenticado, /auth si no)
├── library/       # /library + CRUD + search externa + multi-source matching + backfiller
├── mangadex/      # cliente releasesource.Client contra api.mangadex.org
├── mangasource/   # interfaz Source (búsqueda externa: anilist) — distinto de releasesource
├── migrations/    # runner up-only
├── profile/       # /settings + /settings/username + /settings/export + /settings/delete-account/* + /restore-account
│                  # + /u/{handle} (id legacy → 301 a slug; slug case-insensitive, previous_slug reservado 30d)
│                  # + CleanupWorker (hard delete a los 7 días)
├── ratelimit/     # buckets por clave con golang.org/x/time/rate
├── releases/
│   ├── handler/   # /releases (caps no leídos) + /releases/mark-read (cursor)
│   ├── matcher/   # exact-match normalizado, una instancia por source
│   ├── worker/    # poll periódico, dispatch por source via Clients map + SourceHealth in-memory
│   └── digest/    # render multipart + Sender compartido + Worker (dom 9-22 UTC, retry diferido vía digest_runs)
├── releasesource/ # interfaz Client (search+fetchChapters) + tipos compartidos
├── server/        # http.Server, routing, middleware, security headers, pendingDeletionRedirect
├── session/       # manager + middleware + cookies HttpOnly/Secure/SameSite=Lax
└── unsubscribe/   # signer HMAC + GET/POST /unsubscribe (RFC 8058 one-click)
```

## Deploy en producción

mokuji.org corre en VPS de DigitalOcean (mimir). Binary distribuido como `mokuji-linux-amd64` (13 MB, ELF estático, stripped).

- **systemd**: unit en `/etc/systemd/system/mokuji.service`, `Type=simple`, `User=mokuji`, `StateDirectory=mokuji` (resuelve a `/var/lib/mokuji`)
- **env vars**: `/etc/mokuji/env`, modo `0600 root:root`
- **nginx** en frente, termina TLS, proxy a `127.0.0.1:8080`
- **TLS**: Let's Encrypt vía certbot
- **Email outbound**: Resend en **puerto 2587** (ver gotcha abajo)

### Env vars de producción

```
HTTP_ADDR=127.0.0.1:8080
BASE_URL=https://mokuji.org
SECURE_COOKIES=true
TRUST_PROXY=true
DB_PATH=/var/lib/mokuji/mokuji.db
SMTP_HOST=smtp.resend.com
SMTP_PORT=2587
SMTP_USERNAME=resend
SMTP_PASSWORD=re_<api_key_de_resend>
SMTP_FROM=mokuji <auth@mokuji.org>
SMTP_FROM_PRODUCT=mokuji <releases@mokuji.org>   # opcional; vacío = usa SMTP_FROM. Separa el From de product (digest) del de auth (OTP). OJO: NO da deliverability independiente — mismo dominio, misma alineación SPF/DKIM, misma reputación. La separación real exige subdominios verificados aparte (auth.mokuji.org / mail.mokuji.org); pendiente.

# Fase 2 — required en prod (SECURE_COOKIES=true hace fail-fast si faltan)
UNSUBSCRIBE_SECRET=<openssl rand -base64 32>
DELETION_LOG_PEPPER=<openssl rand -base64 32>

# Fase 2 — opcional (vacío = endpoints admin deshabilitados)
ADMIN_TOKEN=<openssl rand -base64 32>

# v1.0.5 — required en prod (SECURE_COOKIES=true hace fail-fast si faltan)
TURNSTILE_SITE_KEY=<site key de Cloudflare Turnstile>
TURNSTILE_SECRET_KEY=<secret key de Cloudflare Turnstile>

# Fase 3 — sources opt-in (default on)
ASURA_ENABLED=true        # bajar a false para self-hosters conservadores
ASURA_API_BASE_URL=       # vacío = api.asurascans.com (override si rotan dominio)
ASURA_FRONTEND_BASE_URL=  # vacío = asurascans.com (para reconstruir reader URLs)
```

### Update flow (CD automático)

**Merge a `main` = deploy a producción.** Workflow [`deploy.yml`](../.github/workflows/deploy.yml) en GitHub Actions → `make build-linux-amd64` en el runner → binario por SSH a `deploy@mimir.odin.do` (comando forzado `mokuji`). No corre tests: correrlos en local antes del merge. Detalle en [ADR 0015](adr/0015-cd-github-actions.md) (el flujo anterior, con runner de Forgejo, está en [ADR 0012](adr/0012-cicd-forgejo-runner.md), reemplazada). Las migraciones corren automáticamente al startup del binario. Antes de mergear una versión con migración destructiva (como la `0015`) hacer backup de la DB.

Verificación post-deploy:

```bash
curl https://mokuji.org/version   # debe devolver la versión publicada
ssh mimir 'systemctl status mokuji'
ssh mimir 'journalctl -u mokuji -n 30 --no-pager'
```

Fallback manual si el CD falla (el script `mokuji-deploy` es del flujo anterior; confirmar que sigue en mimir):

```bash
make build-linux-amd64
scp ./bin/mokuji-linux-amd64 mimir:/tmp/mokuji
ssh mimir 'sudo /usr/local/bin/mokuji-deploy /tmp/mokuji'
```

### Versionado

- Fuente única: archivo [`VERSION`](../VERSION) en root (ej. `v0.1.0`). Bump = editar + commit.
- Inyectado en build vía `-ldflags="-X .../internal/version.Value=$(cat VERSION)"` (ver [Makefile](../Makefile)).
- Visible en footer de toda página HTML (`mokuji v0.1.0`, link a `/version`) y endpoint `GET /version` (plain text).
- Local (`air`, `make run`, `make migrate`) usa sufijo `-dev` automáticamente (`v0.1.0-dev`) para distinguir de producción.

## Habilidades operativas que ya están

- Rate limit en `/auth/start` (por IP y por email), `/auth/verify` (por IP). IP real detrás de nginx vía `TRUST_PROXY=true` + `X-Forwarded-For`.
- Session cleanup goroutine cada hora.
- Security headers (HSTS, X-Frame-Options, X-Content-Type-Options, Referrer-Policy, Permissions-Policy, CSP estricta — `script-src 'self' https://challenges.cloudflare.com` y `frame-src 'self' https://challenges.cloudflare.com`, única excepción de origen externo, agregada en v1.0.5 para el widget de Turnstile).
- Cookies con Secure (en prod), HttpOnly, SameSite=Lax.
- CSRF protection en todos los POST autenticados.
- Hash de session IDs server-side (defense-in-depth contra DB leak).
- PWA instalable (manifest + service worker).
- Dark mode automático via `prefers-color-scheme`.
- Versión inyectada en build, visible en footer y `/version` (ver sección Versionado arriba).
- CD automático: push a `main` deploya solo (GitHub Actions → SSH a mimir).
- **Workers en-proceso (Fase 2)**: poll mangadex (1h), digest semanal (domingo 9-22 UTC con retry diferido hasta 3 corridas/día vía tabla `digest_runs`), hard delete diario. Todos goroutines en el mismo binario; sin systemd units adicionales.
- **Rate-limited HTTP client** para MangaDex (`golang.org/x/time/rate`, 2 req/s + retry on 429/5xx respetando Retry-After).
- **Email outbound multipart** (plain+HTML básico) para product emails, plain-text para auth (ADR 0007 — nota Fase 2). Headers `List-Unsubscribe` + `List-Unsubscribe-Post` RFC 8058 one-click en cada digest. From separable vía `SMTP_FROM_PRODUCT` (default: comparte `SMTP_FROM` con auth).
- **Resiliencia del digest**: el sender hace 1 retry inmediato con backoff 5s en SMTP transient. El worker tickea cada 1h durante toda la ventana del domingo, usa `digest_runs` para no re-dispatchear si el último run terminó limpio, y reintenta hasta `MaxRunsPerDay=3` si quedaron failures. Los users con `last_digest_sent_at >= startOfDay` se filtran en SQL, evitando duplicados en retries diferidos. Log se sube a `ERROR` cuando `failed > 0`.
- **GDPR compliance**: export JSON al vuelo + soft delete 7d con cancelación self-service + hash one-way audit log.

## Gotchas operativos que aprendimos

Vale dejarlos visibles para que no se rompa de nuevo en futuros deploys:

1. **DigitalOcean bloquea 25, 465 y 587 outbound en todos los Droplets nuevos**. Por eso usamos `SMTP_PORT=2587` con Resend (Resend ofrece 2587 como alternativa STARTTLS exactamente para este caso). Si en el futuro cambiamos provider, verificar que tenga puerto alternativo disponible o pedir unblock a DO.
2. **stdlib `net/smtp.SendMail` solo soporta STARTTLS, no TLS implícito (`:465`)**. Conectar a `:465` cuelga hasta timeout. Usar puertos STARTTLS (587, 2587, etc.).
3. **`<img>` cross-origin desde HTTPS necesita `referrerpolicy="no-referrer"`** para AniList CDN. Sin eso, AniList sirve la imagen en HTTP→HTTPS (downgrade, sin referer) pero rechaza HTTPS→HTTPS (con referer mokuji.org).
4. **`Type=simple` en systemd no espera readiness real**. Para deploys con dependencias hacia futuro, migrar a `Type=notify` con `sd_notify` — anotado en pendientes.

## Backlog priorizado

Detalle en [decisiones-pendientes.md](decisiones-pendientes.md).

### v0.6 — cerrado ✅ (branch `version-0-6`)

Primer minor del roadmap hacia v1.0 "shareable". Cerró landing pública +
ordering de biblioteca.

- **Orden biblioteca por estado** (`internal/library/library.go`): leyendo > por leer > completados, con favorito como tie-break dentro del bucket y `updated_at DESC` como último. Test `TestListEntriesOrderByStatus`.
- **Landing pública** en `/` para visitantes deslogueados (antes redirect a `/auth`). Mobile-first responsive con breakpoint desktop a 1024px. Portado de `dx-marketing.jsx` + `dx-marketing-more.jsx` del handoff Claude Design.
- **OG meta tags por default** en `layout.html` (og:title, og:description, og:image, twitter:card) — variantes específicas por ruta llegan en v0.7. PNG en `web/static/og/og-default.png` (1200×630).
- **app-nav global oculta cuando no hay sesión**: el landing controla su propio top bar editorial; las páginas auth tampoco muestran chrome de app.

### v0.7 — cerrado ✅ (branch `version-0-7`)

Segunda fase del roadmap hacia v1.0. Cerró cara externa completa.

- **Onboarding wizard** (`internal/onboarding/`): 3 pasos post-OTP — cataloga (登) / monitoreamos (新) / digest prefs (始). Gateado por `users.onboarded_at` via middleware `onboardingRedirect`. Backfill setea `created_at` para users existentes (no predatory).
- **Auth redesign** (`web/templates/auth/start.html` + `verify.html`): copy traducido del handoff "magic link" → "código de 6 dígitos OTP". Kanji 封 en verify, hidden email field preservado.
- **Splash PWA** (`web/manifest.webmanifest`): `background_color: #1a1815` (sumi-black) para que iOS renderee splash oscuro con el icon SVG en blanco. `display_override`, `categories`, `dir/lang` explícitos.
- **OG variantes per-route**: 5 PNGs adicionales (`og-release.png`, `og-selfhost.png`, `og-minimal.png`, `og-profile.png`, `og-entry.png` — todos 1200×630) + override del bloque `meta` en `entry.html`, `releases.html`, `profile.html`.
- **Desktop sidebar a >=1024px** (`layout.html`): refactor `body { max-width }` → `.app-shell` grid `16rem 1fr`. Mobile <1024px sigue con nav top inside main. Desktop muestra sidebar lateral fijo con nav + perfil + logout + version.
- **Daily digest backend** (migration `0012` + `internal/releases/digest/worker.go` + `internal/profile/profile.go`): nueva columna `users.digest_frequency` (`weekly`/`daily`/`off`), worker tickea cada hora dentro de 09-22 UTC y dispatch según frequency (`daily` siempre, `weekly` solo domingo). Settings reemplazó el checkbox por radio group de 3 opciones. Onboarding step 3 escribe `digest_frequency` + `onboarded_at` en el mismo POST.

Hotfix v0.7.1: padding lateral en desktop para `.app-shell` sin sidebar (landing y `/auth` quedaban pegadas al edge en pantallas anchas).

### v0.8 — cerrado ✅ (branch `version-0-8`)

Tercera fase del roadmap hacia v1.0. Cerró pulido.

- **Empty states normalizados** (5 templates, 6 kanji): 白 biblioteca vacía / 無 sin coincidencias / 了 al día con releases / 秘 perfil privado / 待 buscando matches / 空 sin capítulos. `unsubscribe.html` movido de `.empty-state` a `.error-page` (semánticamente correcto). `library/entry.html` colapsado de 5 variantes a 2.
- **Microcopy review** (auth): error strings refraseados con orientación accionable ("demasiados intentos. espera un momento" → "demasiados intentos en pocos minutos. esperá un poco y volvé a intentar"). Variantes IP/email del rate limit ahora con copy uniforme al user.
- **5xx estilizados** (`internal/errrender/`): nuevo paquete con `ErrorRenderer.InternalServerError(w, r)`. Wireado en 5 handlers user-facing (home/library/releases/auth/profile, ~38 call sites). Admin endpoints mantienen `http.Error` texto plano.
- **Email templates redesign** (`web/emails/`): digest + otp + account-deletion alineados al handoff (outer cream `#dcd3bd` + paper card `#f3ede0` + header `目次 mokuji` + footer consistente). OTP usa grid de 6 dígitos en celdas table-based para compat con clientes legacy.
- **Favicons completos** (`web/static/`): icon-180/192/512.png + icon-512-maskable.png + favicon.ico multi-resolution (16+32). Manifest declara 5 icons. Layout link tags actualizados (svg primario + PNG 512 fallback + apple-touch 180 + shortcut ICO).
- **Performance** (`layout.html`): `<link rel="preload" as="style">` sobre el CSS de Google Fonts para reducir RTT crítico above-the-fold.
- **Dark mode parity audit**: pasada sobre los 3 `@media (prefers-color-scheme: dark)` blocks. Gap encontrado y arreglado: `button.danger` no tenía override y quedaba apagado en dark. Override con `var(--accent)` + brighter hover.

### v1.0 — cerrado ✅ (branch `version-1-0`)

Cierre del roadmap: el proyecto está listo para promoción pública.

- **README.md rico**: hero (`og-default.png`) + value prop + 3 bullets + stack + sources reales (MangaDex + Asura + Comick) + badges + links a self-host/CONTRIBUTING + AGPL nota.
- **docs/self-hosting.md** (nuevo): guía agnóstica de plataforma. Build, env vars completas con grupos (esenciales / obligatorias en prod / opcionales por feature), principios de deploy (proceso supervisor + reverse proxy + persistencia), ejemplos de systemd y docker-compose sin prescribir uno.
- **CONTRIBUTING.md** (nuevo): setup local, tests, estructura del repo, commits, cuándo ADR, flow de PRs, voz (tuteo dominicano + tono específico).
- **Discoverability técnica**: `/robots.txt` + `/sitemap.xml` + `/llms.txt` servidos desde el root (handler nuevo `mountDiscoverability` en `internal/server/`). JSON-LD `SoftwareApplication` en landing. `<link rel="canonical">` overridable en layout. `BaseURL` agregado a `server.Config`.
- **Página `/privacy`** pública, sin auth: qué se recolecta, por qué, retención, derechos (export/delete), contacto `kg@odin.do`. Link en footer.
- **Sweep voseo**: limpia README y SECURITY que arrastraban voseo del primer commit (mayo 2026).
- **Repo público**: Forgejo abierto a browse anónimo (2026-05-25). Hoy el repo también está en GitHub (`origin`).
- **Bump VERSION a v1.0.0** + tag + release notes en Forgejo.

### v1.0.1 — cerrado ✅ (branch `feat/feedback-form`)

Primer minor post-v1.0. Form de feedback in-app, opt-in vía env var.

- **`/feedback`** auth-only con `tipo` (bug/feature/copy/otro) + `mensaje`. Email a `FEEDBACK_EMAIL_TO` (vacío = feature desactivado, ruta 404, link oculto). Rate limit 5/hora por user_id. `Reply-To` con el email del user (resuelto vía closure DB inyectada en Config — el paquete `feedback` queda libre de `sql`).
- **Link condicional** en footer global, sidebar y `/settings` gateado por template func `feedbackEnabled` que delega en `feedback.IsEnabled()` (flag `atomic.Bool` seteado al startup).
- **Sin DB nueva**, sin captchas third-party.

### v1.0.2 — cerrado ✅ (branch `feat/library-improvements`)

Tres mejoras chicas a la experiencia de biblioteca + sources.

- **Cursor arbitrario** (`POST /library/entry/{id}/cursor`): form numérico "voy por el capítulo X" que setea `library_entries.last_read_chapter_number` sin la guarda monotónica de `markChapterRead`. Permite retroceder (corregir caps marcados tarde) y subir (catalogar manga ya leído afuera). Prefill con el valor actual.
- **Badge "halando capítulos…"** en sources matched sin chapters todavía. `EntrySource.Pending` se calcula vía `LEFT JOIN chapter` + `COUNT()` en `loadEntrySources`; el template renderiza `⏳ halando capítulos…` en lugar de `✓ matched` mientras el poll inicial corre. Cierra el caso Dr. Stone (poll inicial de >100 caps con rate limit toma minutos y el user pensaba que el match había fallado).
- **`MANGADEX_ENABLED` env var** (default `true`) + manejo gracioso del caso all-off: `main.go` arranca normal con un warning, no construye worker ni backfiller. La biblioteca queda como catálogo manual sin tracking automático.

### v1.0.3 — cerrado ✅ (branch `feat/pagination`)

Paginación en `/releases` y en `/library/entry/{id}` chapter list. Resuelve la friction del caso Dr. Stone (232 caps en una sola página) y permite navegar releases acumulados por bloques.

- **Releases**: `UnreadReleasesPage(ctx, userID, limit, cursor) ([]Release, *Cursor, error)` con cursor `(published_at, chapter_id) DESC`. Page size 50 fijo. Fetcha `limit+1` para detectar siguiente sin COUNT extra. El home widget sigue usando `UnreadReleases` sin cursor. `scanReleases` helper compartido evita drift entre queries.
- **Entry chapters**: `?page=N` offset-based. Page size 100. Slicea post-grouping en Go (no en SQL, porque la dedup por `(number, language)` sucede en Go). Clampa `page > totalPages` al último válido vía `min`/`max` builtins.
- **UI**: "← anterior · página N de M · siguiente →" en entry; "siguiente →" único en releases (cursor no soporta back nativamente). Preserva `include_read` y `sort` en los links via `add`/`sub` template funcs.
- **CSS** `.pagination` + `.pagination-link` + `.pagination-current` duplicado en `releases.html` y `entry.html` (cada template tiene su propio `<style>` block; ~15 LOC c/u).

### v1.0.4 — cerrado ✅ (branch `fix/otp-abuse`)

Respuesta a un incidente de **OTP/email bombing**: alguien encontró el `/auth/start` público (aún sin lanzar) y lo floodeó con emails random, agotando la quota diaria de Resend y — más grave — generando bounces que dañan la reputación del dominio como sender.

- **Circuit breaker global de OTP** (`internal/auth/auth.go`): un único bucket (`ratelimit.Limiter` con clave constante `"global"`) para todo el envío de `/auth/start`, chequeado justo antes de `SendMessage`. `~3/hora sostenido, burst 20` → peor caso ~92 envíos/día, bajo el free tier de Resend (100/día). Es lo único que acota un flood distribuido: los límites por-IP y por-email no sirven porque cada IP/email random estrena su propio bucket. Al agotarse: no envía, no escribe el OTP, no redirige a verify, muestra el error genérico (no revela el tope) y loguea `WARN` con el IP.
- **Per-IP de `/auth/start` apretado** burst 5→3.
- **Cleanup goroutine** para el limiter global (TTL 2h), igual que los demás.
- Nuevo campo `auth.Config.StartGlobalLimiter` (nil = sin límite, compat con tests). Tests end-to-end del handler `start` (sender SMTP fake que cuenta envíos): breaker agotado no envía / con presupuesto sí.
- **Sequencing operativo**: deployar este fix **antes** de restaurar la API key de Resend; si no, el abuso vuelve sin tope. Defensa de borde opcional pendiente: `limit_req` en nginx sobre `/auth/start`.

### Sin versionar — visibilidad en el onboarding

Cierra el hallazgo #11b de la auditoría de UX: las cuentas nuevas arrancaban
con la biblioteca pública sin que el onboarding lo dijera. El paso 3 ahora
pregunta "quién puede ver tu biblioteca" (`pública` preseleccionada, `privada`),
y `POST /onboarding/finish` escribe `list_visibility` junto con
`digest_frequency`. Un valor ausente o inválido cae a `public`, que es el
default de la columna. Tests en `internal/onboarding`.

### v1.2.0 — UX y accesibilidad

Implementa las decisiones D1–D8 y A1–A4 de `docs/ux-auditoria.md`
(plan: `docs/superpowers/plans/2026-10-19-ux-v1.1.md`).

**Antes del deploy**: la migración 0015 borra `library_entries.last_chapter`
y no tiene vuelta atrás. Backup de la DB antes de subir, y después de migrar
no se puede volver al binario de v1.1.0 (lee esa columna y da 500).

- **Un solo progreso (D1)**: el cursor reemplaza a `last_chapter`. La
  migración 0015 sube el cursor con los valores numéricos, antepone los no
  numéricos a las notas (`progreso anterior: …`) y borra la columna. El
  export ya no trae `last_chapter`.
- **El detalle gestiona (D2)**: estado con tres botones (`aria-pressed`),
  favorito como toggle, notas y fuente, y borrar en dos pasos con
  `<details>`. Endpoints chicos (`/status`, `/favorite`, `/notes`) que
  redirigen con fragmento; `POST /library/{id}/update` desaparece. La lista
  solo navega (el link es el título, estirado a toda la fila).
- **Estado reactivo (D3)**: cualquier progreso pasa "por leer" a "leyendo".
  Agregar lleva al detalle del manga.
- **Sin home (D4)**: `/` con sesión redirige a `/library`, que ordena lo que
  tiene capítulos sin leer primero. El logout de mobile vive en ajustes.
- **Sources honestas (D5)**: un vocabulario (`N nuevos · al día ·
  buscando… · sin capítulos`) y el detalle por source al final, cerrado.
- **Sigue leyendo (D6)**: "sigue con el cap. N →" arriba; sin fechas de
  subida en la lista de capítulos.
- **Búsqueda (D7)**: formato, año, capítulos y estado desde AniList;
  one-shots y novelas al final.
- **Copy (D8)**: nav `biblioteca · nuevos · ajustes`, ajustes con un form
  por sección, onboarding con las sources de la instancia.
- **Accesibilidad**: contraste AA en claro y oscuro (`--text-muted`,
  `--accent-text`, `--border-input`), skip link, foco visible, labels en
  los filtros, sin auto-submit, errores asociados al campo, kanji
  decorativos ocultos al lector, nombres completos en botones repetidos y
  sin scroll horizontal a 320px. **Arregla un bug de prod**: el `confirm()`
  de borrar era un handler inline que la CSP bloqueaba, así que borrar no
  pedía confirmación.

### v1.1.0 — import de MyAnimeList

Feature nueva: importar el export de manga de MAL desde `/settings`.
Diseño en `docs/superpowers/specs/2026-10-19-import-mal-design.md`.

- **Parser** (`internal/malimport`): `.xml` o `.xml.gz` (gzip por magic
  bytes), lectura en streaming con tope de 20 MB descomprimido (gzip bomb) y
  1000 entradas. Detecta el export de anime y lo rechaza con mensaje propio.
- **Identidad exacta**: MAL id → AniList por `idMal_in`, en lotes de 50
  (`anilist.ByMALIDs`). Lo que no mapea no se adivina por título: sale en el
  resumen como "no encontrado".
- **La biblioteca actual manda**: si el manga ya está, el import solo puede
  subir el cursor; estado, notas, favorito y URL no se tocan. Reimportar el
  mismo archivo no cambia nada. Todo va en una transacción. Estados:
  Reading → leyendo, Plan to Read / On-Hold → por leer, Completed →
  completado, Dropped → no se importa.
- **Matching en carril propio**: lo nuevo en "leyendo" se matchea y se
  pollea de a un manga y en línea (`matchImported`), así esas filas salen
  con `next_check_at` seteado y no se cuelan al frente de la cola del
  worker (50 filas/h), que habría atrasado los releases de todos.
  `matchAll` quedó compartido entre `add` y el import.
- **CSRF con multipart**: el middleware no leía el token de forms con
  archivo (`ParseForm` ignora bodies multipart → 403). Ahora, si el body es
  multipart, lo acota con `MaxBytesReader` (6 MB, 413 si se pasa) y lo
  parsea antes de validar.
- Límite de 3 imports por hora por usuario; el handler extiende su write
  deadline a 55 s. En logs solo van conteos, nunca títulos.

**Validado contra exports reales** (2026-10-23): los tags supuestos coinciden
con el export de manga de MAL (`user_export_type` 2) y el de anime (1) se
rechaza con su mensaje. Una lista real de 69 mangas mapeó 67 de 68 en
AniList (`idMal`); fixtures recortados y anonimizados en
`internal/malimport/testdata/*_real.xml`.

**Pendiente de deploy**: nginx trae `client_max_body_size 1m` por default; para
el tope de 5 MB del import hace falta `client_max_body_size 6m` en
`/settings/import/mal`.

### v1.0.8 — backoff del crawler y polleo en paralelo

Tres arreglos en el worker de release tracking.

- **Backoff persistente por fila** (`migración 0014`, `manga_sources.consecutive_failures`):
  cuando `FetchChapters` fallaba con algo que no fuera rate limit, `pollSource`
  registraba el error y volvía **sin reagendar**. `next_check_at` se quedaba
  como estaba — y para una fila recién matcheada eso es `NULL`. Como
  `dueSources` ordena `next_check_at IS NULL DESC, next_check_at ASC`, esa
  fila quedaba en la primera posición de **todos** los batches, para siempre.
  Una serie borrada upstream o una source con el HTML cambiado se volvía una
  fila envenenada que le robaba el turno a las sanas; con `BatchSize=50`,
  cincuenta de esas y nada vivo se vuelve a pollear. Ahora cada fallo
  incrementa el contador y reagenda con backoff exponencial sobre el recheck
  interval, topado en `MaxBackoff` (24h). Un éxito resetea y limpia
  `last_error`. El rate limit no cuenta como fallo: sigue difiriendo 15 min.
- **Polleo en paralelo por source**: `RunOnce` recorría las 50 filas en serie.
  Con tres hosts independientes y timeouts de 15-20s, uno lento congelaba a
  los otros dos (~16 min en el peor caso). Ahora las filas se agrupan por
  source y cada grupo va en su goroutine; dentro del grupo sigue siendo
  secuencial, que es lo que respeta el rate limit de esa source.
- **Índice `(manga_id, source, published_at DESC)`** en `chapter`:
  `latestPublishedAt` corre una vez por poll filtrando por manga **y** source,
  pero el índice existente solo cubría `manga_id`. El viejo se conserva — lo
  usan `/releases` y el digest.

`setupDB` de los tests pasó de `":memory:"` a un archivo temporal: con
`database/sql` cada conexión nueva a `":memory:"` abre una base vacía
distinta, y con el polleo en paralelo el pool puede abrir más de una.

**Pendiente**: `manga_sources.last_error` y `last_error_at` se escriben pero
todavía no los lee ningún endpoint — hoy solo se consultan por SQL.
Exponerlos en `/admin/sources-health` (que hoy solo muestra estado in-memory,
y por lo tanto se resetea en cada deploy) es el paso natural.

### v1.0.7 — pattern de username y Permissions-Policy

Dos arreglos que salieron de mirar la consola del browser en producción.

- **`pattern` del username roto** (`web/templates/settings.html`): los browsers
  compilan el atributo `pattern` con el flag `v`, donde un `-` literal dentro
  de una clase de caracteres necesita backslash. `[a-z0-9][a-z0-9_-]*[a-z0-9]`
  no compilaba, así que el browser descartaba el pattern y el campo quedaba
  **sin validación de cliente**. No era un hueco de seguridad — `usernameRe`
  en `internal/profile/username.go` lo rechazaba igual en el server — pero el
  user se enteraba al enviar en vez de al escribir. Escapado a `_\-`.
- **`interest-cohort=()` removido del Permissions-Policy**: era el opt-out de
  FLoC, que Chrome retiró. La directiva ya no hace nada y genera un warning
  "Unrecognized feature" en cada carga, que es como uno aprende a ignorar la
  consola.

### v1.0.6 — fuentes self-hosteadas

Arregla un bug que existía desde que se agregó la CSP: `layout.html` cargaba
Albert Sans, IBM Plex Mono y Noto Serif JP desde Google Fonts, pero
`style-src 'self' 'unsafe-inline'` nunca permitió ese stylesheet y `font-src`
ni siquiera estaba declarado (heredaba `'self'`, bloqueando `gstatic`). O sea
que **en producción las fuentes nunca cargaron** y toda la tipografía caía al
fallback del sistema, para todos los visitantes, en todas las páginas. Salió a
la luz por las violaciones de CSP en la consola del browser.

- **Self-hosteadas en `web/static/fonts/`** (76 KB en total), servidas por el
  handler de `/static/` que ya existía. La CSP no se toca: `'self'` ya las
  permite. De paso se va un tercero del render de cada página y deja de
  filtrársele a Google el IP de cada visitante.
- **Albert Sans es variable**: los 5 pesos que pedíamos a Google eran el mismo
  archivo (md5 idéntico), así que va uno solo con `font-weight: 400 900`.
  Verificado que el eje funciona: el mismo texto mide 196.81px a 400 y
  206.03px a 900.
- **IBM Plex Mono** sí son instancias estáticas — tres archivos, uno por peso.
- **Noto Serif JP subseteada a los 16 kanji** que usan las plantillas
  (了始封待新期朱次無登白目秘空蔵迷): 7.8 KB en vez de los megas de la fuente
  completa. El `unicode-range` del `@font-face` los lista explícitamente.
  **Si agregas un kanji nuevo a una plantilla hay que regenerar ese subset**,
  o cae al fallback en silencio.
- Licencias OFL de las tres incluidas junto a los `.woff2`.
- Los `preconnect` a Google se reemplazaron por `preload` de las dos fuentes
  del above-the-fold (Albert Sans y IBM Plex Mono 400).

### v1.0.5 — hardening de OTP + break-glass

Cierra lo que v1.0.4 dejó abierto. El circuit breaker acotaba el volumen a
~92 envíos/día, pero no la raíz: esos 92 seguían yendo a extraños (bounces,
reputación), y el bucket único dejaba que el atacante bloqueara el login de
todos, operador incluido.

- **Break-glass** (`internal/grant`, migración `0013_admin_grants`):
  `POST /admin/grant-session` emite un token single-use de 256 bits (TTL 5
  min, solo el SHA-256 en DB, 404 si el user no existe) y `GET /auth/grant`
  lo canjea por sesión. Rompe la dependencia total del flow de auth con el
  email. `WARN` en emisión y canje.
- **Turnstile** (`internal/turnstile`): reto obligatorio antes del envío, en
  **todo** `/auth/start` — no solo en emails desconocidos, porque la
  presencia del widget sería un oráculo de enumeración. Fail-closed si
  Cloudflare no responde. Requerido con `SECURE_COOKIES=true`.
- **Buckets partidos**: `StartGlobalLimiter` se reemplaza por
  `StartKnownLimiter` (~3/h, burst 10, techo ~72/día) y
  `StartUnknownLimiter` (~1/h, burst 3, techo ~24/día). Un flood de
  direcciones random ya no puede tocar el presupuesto de login de la gente
  con cuenta.
- **Cold-list**: 2+ códigos sin verificar en 24h y se deja de enviar a esa
  dirección. DB-backed, así que sobrevive los reinicios que resetean los
  limiters in-memory — o sea, cada deploy del CD.
- **Reaper de `otp_codes`** (hueco preexistente, no del ataque): nada
  drenaba la tabla salvo `HardDeleteUser`. Goroutine horaria con retención
  de 24h, que es la ventana que necesita la cold-list. Borra también
  `admin_grants` vencidos.
- **Pendiente operativo**: `limit_req` de nginx en `/auth/start`, y
  subdominios de sending separados.
- **Sequencing operativo**: `main.go` hace `log.Fatal` al arrancar si
  `SECURE_COOKIES=true` y faltan `TURNSTILE_SITE_KEY` o
  `TURNSTILE_SECRET_KEY` — y el merge a `main` dispara deploy automático. Si
  el env del server no se actualiza primero, el binario nuevo no levanta.
  Poné las dos keys de Turnstile en el env de prod **antes** de mergear esta
  branch. La migración `0013_admin_grants` es puramente aditiva (tabla
  nueva, no toca nada existente), así que un rollback al binario anterior
  es limpio si algo sale mal.

### Backlog post-v1.0.3

- **Tie-break del matcher con MangaDex/Asura**: hoy si esas sources devuelven >1 exact-match del título, el matcher cae a `needs_review` porque `pickBestExact` necesita `LastChapter`/`LastChapterAt` y solo Comick los popula. Agregar parsing de esos campos en `internal/mangadex/client.go` (API expone `attributes.lastChapter`) resolvería el caso Dr. Stone-tipo donde MD tiene 4 entries que matchean "Dr. Stone". ~10 LOC + test.
- **Activity feed / capa social** sobre `/u/{slug}`.
- **Bato.to como 4ta source** (scaffolding listo).
- **Admin tool `POST /admin/link-source`** (vincular sources manualmente, no user-facing).
- **Exponer `manga_sources.last_error`** en `/admin/sources-health` (v1.0.8).
- **`GET /admin/digest-status`** (observabilidad del worker).
- **Passkeys / WebAuthn** (research, no implementación).
- **`Type=notify` con `sd_notify`** para readiness real en systemd.
- **Soporte SMTP `:465`** (TLS implícito) para self-hosters.
- **CI: tests antes de deployar** (rollbackeado; necesita cache de módulos + timeout explícito).
- **Rating** (puntuación de manga) — explícitamente fuera de MVP, pero queda como espera potencial.
- **MCP server / API pública** (avisos en ChatGPT): descartado por ahora (2026-10-31). MCP es pull y exigiría API pública con OAuth; no encaja con un aviso push.

## Cómo retomar

1. Leer este archivo + [`decisiones-pendientes.md`](decisiones-pendientes.md).
2. `git status`, `git log --oneline -10` para ver último estado del repo.
3. `curl https://mokuji.org/version` para confirmar qué versión corre prod (debe matchear `cat VERSION` del último commit en `main`).
4. `ssh mimir 'systemctl status mokuji'` para verificar producción si vale.
5. Mantener el patrón establecido: una decisión a la vez con confirmación; ADR cuando sea arquitectónica; commits chicos con subject corto y en español.
