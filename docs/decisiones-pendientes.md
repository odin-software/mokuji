# Decisiones pendientes

Lista de decisiones arquitectónicas aún abiertas, en orden tentativo de discusión. Se va recortando a medida que se cierran ADRs.

## Rate limiting

Crítico antes de exponer el dominio público. Mínimo:

- `/auth/start` rate limit por IP (spam de OTPs a inboxes ajenos)
- `/auth/start` rate limit por email (evita que alguien enumere)
- `/auth/verify` rate limit por IP (intentos de brute force)
- `/unsubscribe` rate limit por IP (evita fuzzing de tokens HMAC — cada POST hace un Verify + UPDATE, cheap pero acumula)

Implementación candidata: counter en memoria con `golang.org/x/time/rate` por IP, complementada con tabla SQLite para tracking por email si lo necesitamos. Decisión propia, no requiere lib externa.

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

## Soporte SMTP `:465` (TLS implícito)

Hoy `internal/email` usa stdlib `net/smtp.SendMail`, que solo soporta STARTTLS. Si un self-hoster quiere usar `:465` (TLS implícito desde el primer byte), no funciona — cuelga.

Para soportarlo: detectar puerto 465 (o un flag `SMTP_TLS_MODE=implicit`) y hacer `tls.Dial` antes de `smtp.NewClient` en vez de delegar a `SendMail`. ~30 líneas extra.

Prioridad: baja. `:587` y puertos alternativos STARTTLS (`:2587` Resend, `:2525` muchos providers) cubren todos los casos comunes.

## CI: correr tests en el workflow antes de deployar

Hoy `.forgejo/workflows/deploy.yml` solo hace `make build-linux-amd64` y llama a `mokuji-deploy`. No corre el suite de tests. Eso significa que un push a `main` con tests rotos igual deploya — el binario buildea pero la lógica puede estar quebrada.

Cambio mínimo: agregar un step antes del build:

```yaml
- name: Run tests
  run: go test ./...
```

Si `go test` no está disponible en el host del runner, instalar Go o usar un container action. Prioridad: media — bajo riesgo hoy porque los pushes a main pasan revisión local, pero antes de que más gente contribuya conviene cerrarlo.

## Pendientes de explicación (no son decisiones, son aprendizaje)

- **Passkeys / WebAuthn**: cómo se implementaría aunque no se haga ahora. Cubrir registration vs authentication ceremony, origin-binding criptográfico, almacenamiento server-side de public keys, recuperación multi-dispositivo, fallback path si el usuario pierde acceso a todos sus passkeys.
