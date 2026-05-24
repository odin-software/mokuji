# Decisiones pendientes

Lista de decisiones arquitectónicas aún abiertas, en orden tentativo de discusión. Se va recortando a medida que se cierran ADRs.

## Rate limiting

✅ Cubierto al cierre de Fase 2/3:

- `/auth/start` por IP y por email
- `/auth/verify` por IP
- `/unsubscribe` por IP (GET y POST, ambos hacen Verify HMAC)

Implementación: `golang.org/x/time/rate` con buckets por clave + cleanup goroutine, en `internal/ratelimit`. Buckets in-memory — aceptable para single-process. Si en el futuro escalamos horizontalmente, pasar a Redis o tabla SQLite (no urgente, mokuji es single-binary por diseño).

## Marca / identidad visual

Pendiente. Una vez decidida condiciona:

- HTML email templates (alternative al plain-text actual — ver [ADR 0007](adr/0007-email-transport.md))
- CSS / theming de la app
- Favicon, OG images, iconos de PWA, manifest

## CSS

Plano + variables custom vs framework (Tailwind, etc.). Probablemente plano para empezar, evaluar cuando duela. Depende parcialmente de marca.

## Flash messages

No se resuelven dentro del package de sessions ([ADR 0005](adr/0005-sessions-propias.md)). Diseño pendiente para cuando aparezca el primer redirect que requiera feedback al usuario (probablemente al primer form de mutación). Patrón candidato: tabla `flash_messages` o cookie firmada de corta duración.

## systemd: migrar a `Type=notify`

Hoy el unit usa `Type=simple` — systemd asume "arrancó" cuando el proceso existe, sin esperar readiness real (que el servidor esté escuchando en el port). Para deploys con dependencias o zero-downtime restarts conviene `Type=notify` con `sd_notify`:

- Agregar dep `github.com/coreos/go-systemd/v22/daemon`
- Después de `srv.Start()` (o cuando ListenAndServe esté servido), llamar `daemon.SdNotify(false, daemon.SdNotifyReady)`
- Cambiar `Type=simple` a `Type=notify` en el unit

Prioridad: baja hasta que tengamos zero-downtime restart o dependencias systemd.

## Username flow (cerrado)

✅ `/u/{numeric_id}` ahora se llama `/u/{handle}` y acepta dos formatos:

- handle numérico = id legacy; si el user tiene slug, 301 a `/u/{slug}` (canonicaliza).
- handle no-numérico = slug; lookup case-insensitive contra `users.slug`.

Reglas del slug: 3-20 chars, `[a-z0-9_-]`, no empieza/termina con separador, no puede ser solo dígitos (chocaría con id legacy), no reservado. Cambio permitido con cooldown de 30 días; el slug viejo queda reservado el mismo lapso (anti-impersonation). UI: banner en home + form en `/settings`.

Pendiente futuro: incluir el slug en `internal/library` y otros templates donde hoy se muestra `userID`. Ahora muchos links a perfil propio usan `Session.UserID` — caen al redirect 301 pero no se ven feos como `/u/12`. Cosmético, no urgente.

## Observabilidad del digest worker

Hoy `digest_runs` registra cada corrida (started_at, finished_at, counters). Para diagnosticar sin SSH conviene un endpoint admin:

- `GET /admin/digest-status` (bearer auth, mismo `ADMIN_TOKEN`) que devuelva JSON con las últimas N corridas + cómputo de "próxima ventana esperada" en función de hoy.

Prioridad: media. Mientras tanto, queries directas: `sqlite3 ... 'SELECT * FROM digest_runs ORDER BY id DESC LIMIT 10'`. Estimado: ~30 LOC.

## Soporte SMTP `:465` (TLS implícito)

Hoy `internal/email` usa stdlib `net/smtp.SendMail`, que solo soporta STARTTLS. Si un self-hoster quiere usar `:465` (TLS implícito desde el primer byte), no funciona — cuelga.

Para soportarlo: detectar puerto 465 (o un flag `SMTP_TLS_MODE=implicit`) y hacer `tls.Dial` antes de `smtp.NewClient` en vez de delegar a `SendMail`. ~30 líneas extra.

Prioridad: baja. `:587` y puertos alternativos STARTTLS (`:2587` Resend, `:2525` muchos providers) cubren todos los casos comunes.

## CI: tests en el workflow antes de deployar

✅ Cerrado. `.forgejo/workflows/deploy.yml` ahora corre `go test ./...` antes del build. Push con suite roja aborta el deploy.

## Sources adicionales para multi-source

Fase 3 cerró con MangaDex (siempre on) + Asura (opt-in default on). Comick.app
quedó descartado de v1 porque su API real (`comick.dev`) está atrás de
Cloudflare managed challenge — un cliente Go vanilla recibe un HTML
"Just a moment…" en lugar de JSON. Reabrir cuando:

- Comick relaje el bot detection (puede pasar si reciben quejas de
  apps móviles legítimas que sufren el mismo bloqueo), o
- Aceptemos depender de un sidecar headless tipo FlareSolverr — cambia
  el shape de deploy (mokuji deja de ser un binario solo) y el self-hoster
  pasa a necesitar Docker.

Otras candidatas en backlog: Bato.to (HTML scraping, overlap con MD),
Reaper Scans (overlap con Asura). Pegarlas se simplifica con el scaffolding
de `internal/releasesource` ya cerrado en ADR 0014.

## Vincular manualmente una source

`/library/entry/{id}` ya muestra el fieldset de sources con reintentar y
desvincular (ADR 0014 §6). Falta el flow para "vincular manualmente"
cuando el matcher devuelve `needs_review` o `no_results` pero el manga
sí existe en la source con un título completamente distinto al de
AniList. UI candidata:

- Botón "vincular manualmente" en cada source con status != matched
- Mini-form que abre `?source=X&q=<title editable>` → llama
  `SourceClient.Search` → lista candidatos con cover + título
- User elige un candidato → UPSERT manga_sources con
  `source_id=elegido, status=matched`

Estimado ~150 LOC + tests. Bajo prioridad: el matching automático
contra alt_titles cubre el 90% de los casos reales.

## Pendientes de explicación (no son decisiones, son aprendizaje)

- **Passkeys / WebAuthn**: cómo se implementaría aunque no se haga ahora. Cubrir registration vs authentication ceremony, origin-binding criptográfico, almacenamiento server-side de public keys, recuperación multi-dispositivo, fallback path si el usuario pierde acceso a todos sus passkeys.
