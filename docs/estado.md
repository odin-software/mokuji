# Estado actual de mokuji

Snapshot del proyecto pensado para retomar después de tiempo (o de un context compact).
Para detalle de decisiones cerradas, ver [`adr/`](adr/). Para lo que falta decidir/implementar, ver [`decisiones-pendientes.md`](decisiones-pendientes.md).

## En una línea

Tracker de manga público en Go + SQLite. MVP completo y deployado en **mokuji.org**. Flow funcional end-to-end: signup con OTP → biblioteca con search de AniList → CRUD por entry → privacy toggle → vista pública `/u/{id}`. PWA instalable.

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
| Fase 2 — Release tracking | MangaDex worker + cursor + email digest + GDPR export/delete | [0013](adr/0013-fase-2-release-tracking.md) (🚧 en branch) |

## Mapa de paquetes

```
internal/
├── auth/          # /auth, /auth/start, /auth/verify, /auth/logout
├── csrf/          # middleware + helper de template
├── email/         # cliente SMTP plain-text
├── home/          # GET / (redirect a /library si autenticado, /auth si no)
├── library/       # /library + CRUD + search externa
├── mangasource/   # interfaz Source + impl anilist/
├── migrations/    # runner up-only
├── profile/       # /settings + /u/{id} público
├── ratelimit/     # buckets por clave con golang.org/x/time/rate
├── server/        # http.Server, routing, middleware, security headers
└── session/       # manager + middleware + cookies HttpOnly/Secure/SameSite=Lax
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

## Gotchas operativos que aprendimos

Vale dejarlos visibles para que no se rompa de nuevo en futuros deploys:

1. **DigitalOcean bloquea 25, 465 y 587 outbound en todos los Droplets nuevos**. Por eso usamos `SMTP_PORT=2587` con Resend (Resend ofrece 2587 como alternativa STARTTLS exactamente para este caso). Si en el futuro cambiamos provider, verificar que tenga puerto alternativo disponible o pedir unblock a DO.
2. **stdlib `net/smtp.SendMail` solo soporta STARTTLS, no TLS implícito (`:465`)**. Conectar a `:465` cuelga hasta timeout. Usar puertos STARTTLS (587, 2587, etc.).
3. **`<img>` cross-origin desde HTTPS necesita `referrerpolicy="no-referrer"`** para AniList CDN. Sin eso, AniList sirve la imagen en HTTP→HTTPS (downgrade, sin referer) pero rechaza HTTPS→HTTPS (con referer mokuji.org).
4. **`Type=simple` en systemd no espera readiness real**. Para deploys con dependencias hacia futuro, migrar a `Type=notify` con `sd_notify` — anotado en pendientes.

## Backlog priorizado

Detalle en [decisiones-pendientes.md](decisiones-pendientes.md). Orden actual:

1. **Fase 2 — Release tracking** — 🚧 **EN PROGRESO** en branch `fase-2-release-tracking`. Diseño cerrado en [ADR 0013](adr/0013-fase-2-release-tracking.md) (destilado de [docs/diseno-fase-2.md](diseno-fase-2.md)). Incluye chapter linking (cursor por entry) y export+delete de data como must-have GDPR.
2. **Usernames** — hoy `/u/{numeric_id}`, bloqueador para launch público real.
3. **Marca / identidad visual** — habilita HTML emails enriquecidos (más allá del HTML básico de fase 2) y CSS de verdad.
4. **`Type=notify` con `sd_notify`** — readiness real para systemd.
5. **Soporte SMTP `:465`** — TLS implícito en lugar de STARTTLS. Bajo.
6. **Activity feed público** — nice-to-have.
7. **Rating** — explícitamente fuera de MVP.

## Cómo retomar

1. Leer este archivo + [`decisiones-pendientes.md`](decisiones-pendientes.md).
2. `git status`, `git log --oneline -10` para ver último estado del repo.
3. `curl https://mokuji.org/version` para confirmar qué versión corre prod (debe matchear `cat VERSION` del último commit en `main`).
4. `ssh mimir 'systemctl status mokuji'` para verificar producción si vale.
5. Mantener el patrón establecido: una decisión a la vez con confirmación; ADR cuando sea arquitectónica; commits chicos con subject corto y en español.
