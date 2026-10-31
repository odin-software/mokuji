# Decisiones pendientes

Lo que sigue abierto, en orden tentativo de discusión, y al final lo descartado a propósito. Se recorta a medida que se cierran ADRs. Última revisión: 2026-10-31.

## Rate limiting

✅ Cubierto al cierre de Fase 2/3:

- `/auth/start` por IP y por email
- `/auth/verify` por IP
- `/unsubscribe` por IP (GET y POST, ambos hacen Verify HMAC)

Implementación: `golang.org/x/time/rate` con buckets por clave + cleanup goroutine, en `internal/ratelimit`. Buckets in-memory — aceptable para single-process. Si en el futuro escalamos horizontalmente, pasar a Redis o tabla SQLite (no urgente, mokuji es single-binary por diseño).

## Marca / identidad visual

✅ Cerrado en `branding` (mayo 2026). Identidad **"01 × 02-type"** (editorial library: vermillón + cream + sumi-black, Albert Sans + Noto Serif JP + IBM Plex Mono, kanji 目次 como base del wordmark). Fases 1–4 aplicadas: tokens + fuentes + icono + manifest, plantillas auth/library/releases/profile/settings/entry/search/404/500, emails HTML opt-in (OTP, account-deletion, digest).

Ver [`diseno-identidad.md`](diseno-identidad.md) para resumen + cambios de backend que la implementación requirió (`homeData.Username`, `profileData.Counters`, `errorRenderer`, multipart emails).

El layout desktop con sidebar y las OG cards se cerraron en v0.7. Pendiente (no urgente): onboarding swipeable.

## CSS

Plano + variables custom decidido al implementar el branding. Todos los tokens viven como CSS custom properties en `:root` de `layout.html`, con override en `@media (prefers-color-scheme: dark)`. Sin framework. Si en algún momento duele (CSS-in-JS, design system de N componentes), revisitamos.

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

Pendiente (cosmético): `layout.html` ("tu perfil") y el texto de `settings.html` sin username siguen enlazando `/u/{{.Session.UserID}}`; caen al redirect 301 al slug. `library/list.html` y el resto de `settings.html` ya usan el slug.

## Observabilidad del digest worker

Hoy `digest_runs` registra cada corrida (started_at, finished_at, counters). Para diagnosticar sin SSH conviene un endpoint admin:

- `GET /admin/digest-status` (bearer auth, mismo `ADMIN_TOKEN`) que devuelva JSON con las últimas N corridas + cómputo de "próxima ventana esperada" en función de hoy.

Prioridad: media. Mientras tanto, queries directas: `sqlite3 ... 'SELECT * FROM digest_runs ORDER BY id DESC LIMIT 10'`. Estimado: ~30 LOC.

## Soporte SMTP `:465` (TLS implícito)

Hoy `internal/email` usa stdlib `net/smtp.SendMail`, que solo soporta STARTTLS. Si un self-hoster quiere usar `:465` (TLS implícito desde el primer byte), no funciona — cuelga.

Para soportarlo: detectar puerto 465 (o un flag `SMTP_TLS_MODE=implicit`) y hacer `tls.Dial` antes de `smtp.NewClient` en vez de delegar a `SendMail`. ~30 líneas extra.

Prioridad: baja. `:587` y puertos alternativos STARTTLS (`:2587` Resend, `:2525` muchos providers) cubren todos los casos comunes.

## CI: tests antes de deployar

Sigue sin haber tests en el workflow. En v0.4.0 se intentó con el runner de Forgejo en mimir y se canceló por recursos (`context canceled, signal: terminated`). Desde el [ADR 0015](adr/0015-cd-github-actions.md) el deploy corre en un runner de GitHub, así que ese problema ya no aplica: agregar `go test ./...` como step antes de compilar (con el cache de módulos que trae `actions/setup-go`) es barato y evita que un merge roto llegue a producción. Hoy los tests se corren en local antes del merge.

Prioridad: media. El merge a `main` despliega directo.

Relacionado: `paths-ignore: '*.md'` solo cubre markdown de la raíz, así que un cambio en `docs/*.md` también despliega. Usar `'**.md'` si no se quiere.

## Sources adicionales para multi-source

Hoy hay tres: MangaDex (siempre on), Asura y Comick (opt-in, default on). Comick se había descartado porque `comick.dev` está atrás de un managed challenge de Cloudflare; se resolvió usando el subdominio `comick.art`, que expone la misma API sin challenge (ver `internal/comick`). Si Cloudflare lo cierra, la alternativa sería un sidecar tipo FlareSolverr, que rompe el "un solo binario".

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

## Descartado a propósito

- **MCP server / API pública** para recibir avisos en ChatGPT (feedback F3, 2026-10-19; descartado 2026-10-31). MCP es pull: el cliente llama tools, el server no empuja notificaciones, así que no resuelve "avisarme". Además exigiría una API pública nueva con OAuth. Reabrir solo si aparece demanda real de consultar la biblioteca desde un asistente.
- **Cambio de estado desde la lista de `/library`** (D2 de la auditoría de UX): vuelve a llenar las filas de controles. Se reabre si llega como queja real.
- **Sugerir "completado" al llegar al último capítulo** (D3): casi siempre es "al día", no "terminado", y no guardamos el estado de publicación.
