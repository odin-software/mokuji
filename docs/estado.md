# Estado actual de mokuji

Snapshot del proyecto pensado para retomar después de tiempo (o de un context compact).
Para detalle de decisiones cerradas, ver [`adr/`](adr/). Para lo que falta decidir/implementar, ver [`decisiones-pendientes.md`](decisiones-pendientes.md).

## En una línea

Tracker de manga público en Go + SQLite. Fase 3 (multi-source release tracking: MangaDex + Asura Scans, scaffolding para más) en branch `fase-3-multi-source`, lista para merge a `main`. Producción `mokuji.org` corre v0.2.0; el bump a v0.3.0 va con el merge. Flow end-to-end: signup con OTP → biblioteca con search de AniList → matching automático contra cada source configurada → caps nuevos en `/releases` + email digest semanal → privacy/digest toggle → export JSON → soft delete con gracia 7 días + fieldset de sources por manga con reintentar/desvincular.

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
| CI/CD | Forgejo runner host-based + script con sudoers | [0012](adr/0012-cicd-forgejo-runner.md) |
| Fase 2 — Release tracking | MangaDex worker + cursor + email digest + GDPR export/delete | [0013](adr/0013-fase-2-release-tracking.md) (✅ mergeada en v0.2.0) |
| Fase 3 — Multi-source | Tabla `manga_sources` + `releasesource.Client` + cliente Asura + backfill | [0014](adr/0014-fase-3-multi-source.md) (✅ implementado, pre-merge) |

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
├── profile/       # /settings + /settings/export + /settings/delete-account/* + /restore-account + /u/{id}
│                  # + CleanupWorker (hard delete a los 7 días)
├── ratelimit/     # buckets por clave con golang.org/x/time/rate
├── releases/
│   ├── handler/   # /releases (caps no leídos) + /releases/mark-read (cursor)
│   ├── matcher/   # exact-match normalizado, una instancia por source
│   ├── worker/    # poll periódico, dispatch por source via Clients map + SourceHealth in-memory
│   └── digest/    # render multipart + Sender compartido + Worker semanal (dom 9am UTC)
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

# Fase 2 — required en prod (SECURE_COOKIES=true hace fail-fast si faltan)
UNSUBSCRIBE_SECRET=<openssl rand -base64 32>
DELETION_LOG_PEPPER=<openssl rand -base64 32>

# Fase 2 — opcional (vacío = endpoints admin deshabilitados)
ADMIN_TOKEN=<openssl rand -base64 32>

# Fase 3 — sources opt-in (default on)
ASURA_ENABLED=true        # bajar a false para self-hosters conservadores
ASURA_API_BASE_URL=       # vacío = api.asurascans.com (override si rotan dominio)
ASURA_FRONTEND_BASE_URL=  # vacío = asurascans.com (para reconstruir reader URLs)
```

### Update flow (CD automático)

Push a `main` → workflow [`deploy.yml`](../.forgejo/workflows/deploy.yml) en Forgejo runner host-based (corre en mimir) → `make build-linux-amd64` → `sudo /usr/local/bin/mokuji-deploy <binario>` (validación + backup `mokuji.prev` + install atómico + restart + health check). Detalle completo y razones en [ADR 0012](adr/0012-cicd-forgejo-runner.md). Las migraciones corren automáticamente al startup del binario.

Verificación post-deploy:

```bash
curl https://mokuji.org/version   # debe devolver la versión publicada
ssh mimir 'systemctl status mokuji'
ssh mimir 'journalctl -u mokuji -n 30 --no-pager'
```

Fallback manual si el CD falla:

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
- Security headers (HSTS, X-Frame-Options, X-Content-Type-Options, Referrer-Policy, Permissions-Policy, CSP estricta — `script-src 'self'`).
- Cookies con Secure (en prod), HttpOnly, SameSite=Lax.
- CSRF protection en todos los POST autenticados.
- Hash de session IDs server-side (defense-in-depth contra DB leak).
- PWA instalable (manifest + service worker).
- Dark mode automático via `prefers-color-scheme`.
- Versión inyectada en build, visible en footer y `/version` (ver sección Versionado arriba).
- CD automático: push a `main` deploya solo (Forgejo runner en mimir).
- **Workers en-proceso (Fase 2)**: poll mangadex (1h), digest semanal (dom 9am UTC), hard delete diario. Todos goroutines en el mismo binario; sin systemd units adicionales.
- **Rate-limited HTTP client** para MangaDex (`golang.org/x/time/rate`, 2 req/s + retry on 429/5xx respetando Retry-After).
- **Email outbound multipart** (plain+HTML básico) para product emails, plain-text para auth (ADR 0007 — nota Fase 2). Headers `List-Unsubscribe` + `List-Unsubscribe-Post` RFC 8058 one-click en cada digest.
- **GDPR compliance**: export JSON al vuelo + soft delete 7d con cancelación self-service + hash one-way audit log.

## Gotchas operativos que aprendimos

Vale dejarlos visibles para que no se rompa de nuevo en futuros deploys:

1. **DigitalOcean bloquea 25, 465 y 587 outbound en todos los Droplets nuevos**. Por eso usamos `SMTP_PORT=2587` con Resend (Resend ofrece 2587 como alternativa STARTTLS exactamente para este caso). Si en el futuro cambiamos provider, verificar que tenga puerto alternativo disponible o pedir unblock a DO.
2. **stdlib `net/smtp.SendMail` solo soporta STARTTLS, no TLS implícito (`:465`)**. Conectar a `:465` cuelga hasta timeout. Usar puertos STARTTLS (587, 2587, etc.).
3. **`<img>` cross-origin desde HTTPS necesita `referrerpolicy="no-referrer"`** para AniList CDN. Sin eso, AniList sirve la imagen en HTTP→HTTPS (downgrade, sin referer) pero rechaza HTTPS→HTTPS (con referer mokuji.org).
4. **`Type=simple` en systemd no espera readiness real**. Para deploys con dependencias hacia futuro, migrar a `Type=notify` con `sd_notify` — anotado en pendientes.

## Backlog priorizado

Detalle en [decisiones-pendientes.md](decisiones-pendientes.md). Orden actual:

1. **Merge `fase-3-multi-source` → `main`** — feature completa, suite verde. Pre-merge: bump VERSION ya está en `v0.3.0`. `ASURA_ENABLED=true` queda implícito (default); el operador puede setear `ASURA_API_BASE_URL` si Asura rota dominio. La migration 0007 corre automática al startup; el backfill recoge mangas existentes en biblioteca.
2. **Widget "últimos releases" en home** — postergado de Fase 2 (la página `/releases` cubre el caso 80%).
3. **Rate limit en `/unsubscribe`** — anotado en `decisiones-pendientes.md` (cheap por request pero acumula sin tope).
4. **Tests en el deploy workflow** — anotado, no corre `go test` antes de deployar.
5. **Usernames** — hoy `/u/{numeric_id}`, bloqueador para launch público real.
6. **Marca / identidad visual** — habilita HTML emails enriquecidos (más allá del HTML básico de fase 2) y CSS de verdad.
7. **`Type=notify` con `sd_notify`** — readiness real para systemd.
8. **Soporte SMTP `:465`** — TLS implícito en lugar de STARTTLS. Bajo.
9. **Activity feed público** — nice-to-have.
10. **Rating** — explícitamente fuera de MVP.

## Cómo retomar

1. Leer este archivo + [`decisiones-pendientes.md`](decisiones-pendientes.md).
2. `git status`, `git log --oneline -10` para ver último estado del repo.
3. `curl https://mokuji.org/version` para confirmar qué versión corre prod (debe matchear `cat VERSION` del último commit en `main`).
4. `ssh mimir 'systemctl status mokuji'` para verificar producción si vale.
5. Mantener el patrón establecido: una decisión a la vez con confirmación; ADR cuando sea arquitectónica; commits chicos con subject corto y en español.
