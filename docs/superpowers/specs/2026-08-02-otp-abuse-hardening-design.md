# v1.0.5 — Hardening anti-abuso de OTP + break-glass

Fecha: 2026-08-02
Estado: diseño aprobado, pendiente de plan de implementación

## Contexto

El 2026-06-04 un atacante encontró `/auth/start` (mokuji.org aún sin lanzar) y lo
floodeó con direcciones de email random. Dos daños:

1. **Quota**: agotó el free tier diario de Resend.
2. **Reputación** — el grave: cada envío fue a un extraño que nunca pidió nada.
   Bounces y spam complaints contra el dominio `mokuji.org`.

La respuesta inmediata (v1.0.4, ya en prod) fue un circuit breaker global de OTP.
El operador además borró la API key de Resend, lo que dejó el login **inutilizable
incluso para él**: no existe ningún camino de acceso que no pase por email.

Este spec cierra las tres cosas que v1.0.4 dejó abiertas: recuperar acceso, atacar
la raíz en vez del volumen, y limpiar el rastro.

## Estado verificado

- Prod corre **v1.0.4** (`curl /version`), o sea el circuit breaker está desplegado.
- La cuenta de Resend está **sana**: no hubo suspensión ni warning formal. Solo
  falta emitir una key nueva.
- `data/mokuji.db` (dev local) está limpia: 3 users, 6 códigos, todos usados. El
  rastro del ataque está solo en prod.
- La DB de prod (`/var/lib/mokuji/mokuji.db`) es legible solo por `root`/`mokuji`;
  los conteos se recogen manualmente antes de ejecutar la purga.

### Por qué `users` no está contaminada

`findOrCreateUser` ([`internal/auth/auth.go:346`](../../../internal/auth/auth.go))
corre **después** de que `consumeCode` valida el OTP. Crear cuenta exige poseer el
código de 6 dígitos entregado a la bandeja. El atacante usó esas direcciones como
destino, no como propias — no pudo verificar ninguna. Los users existentes son
signups reales o pruebas del operador.

La contaminación está en **`otp_codes`**: una fila por intento, con el email de la
víctima, la IP del atacante y el user agent.

## Por qué v1.0.4 no alcanza

El breaker acota el volumen a ~92 envíos/día, pero deja dos agujeros:

- **Reputación**: 92 correos diarios a extraños siguen generando bounces. El
  breaker limita el radio de la explosión; el veneno sigue fluyendo.
- **Disponibilidad**: el bucket es único y compartido. El atacante se lo come y
  los usuarios reales — incluido el operador — no pueden entrar. v1.0.4 convirtió
  un DoS de presupuesto en un DoS de acceso.

## Decisiones tomadas

| Decisión | Elección | Descartado |
|---|---|---|
| Superficie de signup | Abierta, con reto antes del envío | Invite-only; cierre total; solo apretar límites |
| Mecanismo del reto | Turnstile únicamente | PoW propio; PoW con Turnstile opt-in |
| Break-glass | Endpoint admin con `ADMIN_TOKEN` | Subcomando CLI; OTP al log |
| Turnstile ausente | Requerido si `SECURE_COOKIES=true`; opcional en local | Fail-closed siempre; opcional siempre |

## Parte 1 — Break-glass: `POST /admin/grant-session`

Va en el paquete `internal/admin`, que ya existe y ya compara `ADMIN_TOKEN` en
tiempo constante.

- **Request**: `{"email": "..."}`.
- **Si el user no existe: 404.** El endpoint no crea cuentas. Eso acota el daño de
  un token filtrado a "impersonar cuentas existentes" en vez de "fabricar cuentas".
- Genera un token random de 256 bits. Guarda **solo el SHA-256** en la tabla nueva
  `admin_grants` — mismo patrón que `otp_codes` y `sessions.id_hash`.
- **TTL 5 minutos, single-use.**
- **Response**: `{"url": "<BASE_URL>/auth/grant?t=<token>"}`.

**`GET /auth/grant?t=...`** (en `internal/auth`): valida hash, no usado y no
vencido → marca `used_at` **antes** de crear la sesión → `sessions.Create` →
redirect a `/`. Marcar antes evita que un error en `Create` deje el token vivo.

Blindaje, porque esto es un bypass de auth completo:

- Rate limiter propio en el endpoint: `ratelimit.New(rate.Every(time.Minute), 5)`
  por IP. Generoso para uso legítimo, techo duro contra fuerza bruta del token.
- `logger.Warn` en **cada emisión y cada canje**, con IP y email. Un `ADMIN_TOKEN`
  robado sigue siendo game over, pero deja rastro.

**Riesgo aceptado**: el token viaja en query string y aterriza en los access logs
de nginx. Con single-use + TTL de 5 minutos, replayearlo exige ya estar dentro de
la caja.

### Migración `0013_admin_grants.sql`

```sql
CREATE TABLE admin_grants (
    token_hash TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
);

CREATE INDEX idx_admin_grants_expires ON admin_grants(expires_at);
```

## Parte 2 — Defensas en `/auth/start`

### a) Turnstile

Paquete nuevo `internal/turnstile`: render del widget + verificación contra
`siteverify`. Env vars `TURNSTILE_SITE_KEY` y `TURNSTILE_SECRET_KEY`.

Con `SECURE_COOKIES=true` (o sea, prod) son **requeridas** y el arranque hace
fail-fast si faltan — mismo idiom que `UNSUBSCRIBE_SECRET` y `DELETION_LOG_PEPPER`.
En local son opcionales; sin ellas no hay reto, para que Mailpit y los tests sigan
andando sin configuración.

**El reto aplica a todo `/auth/start`, no solo a emails desconocidos.** Si el
widget apareciera únicamente para desconocidos, su sola presencia revelaría si una
dirección tiene cuenta — un oráculo de enumeración gratis. Un solo camino, sin
oráculo, y de paso sin dos ramas que testear.

**Costo aceptado conscientemente**: mete JS de Cloudflare en el flujo de login y
obliga a todo self-hoster a sacar keys, lo que choca con que mokuji sea
self-hosteable sin dependencias externas. El operador evaluó el trade-off y
priorizó defensa fuerte contra un atacante real y presente.

### b) Partir el breaker global en dos buckets

Reemplaza a `StartGlobalLimiter` por dos limiters con la misma mecánica:

| bucket | rate | burst | techo/día |
|---|---|---|---|
| email ya registrado en `users` | 1 cada 20 min | 10 | ~72 |
| email desconocido | 1 cada 60 min | 3 | ~24 |

Suman ~96/día, debajo del free tier de 100. La propiedad que importa: un flood de
direcciones random consume **solo** el bucket de desconocidos. El presupuesto de
login de los usuarios reales queda intacto, y el correo a extraños baja de 92/día
a 24/día — volumen de bounces despreciable.

La decisión de qué bucket usar sale de un `SELECT 1 FROM users WHERE email = ?`
antes del envío. Ese query no cambia la respuesta HTTP ni el timing observable de
forma útil (ambos caminos renderizan el mismo redirect a `/auth/verify`).

### c) Cold-list de direcciones quemadas

Antes de enviar a un email desconocido: si ya tiene **2 o más** códigos en las
últimas 24h con `used_at IS NULL`, no se envía. El atacante puede quemar cada
dirección una vez, no en bucle.

Cero tablas nuevas, cero memoria: sale de `otp_codes`. Y a diferencia de los
limiters actuales — todos in-memory, o sea que se resetean en cada `systemctl
restart`, o sea en **cada deploy del CD** — esta defensa sobrevive reinicios.

El umbral es 2 y no 1 para no castigar al usuario legítimo cuyo primer correo cayó
en spam y reintenta.

## Parte 3 — Limpieza de datos

### a) Reaper periódico de `otp_codes`

**Hueco preexistente, no causado por el ataque**: no existe ningún reaper de
`otp_codes`. `sessions` tiene su goroutine ([`main.go:500`](../../../main.go));
`otp_codes` solo se borra dentro de `HardDeleteUser` cuando alguien elimina su
cuenta. La tabla crece indefinidamente desde el día uno. El ataque nada más la
infló lo suficiente para que se notara.

Goroutine con el mismo patrón que `cleanupSessions`, tick horario:

```sql
DELETE FROM otp_codes WHERE expires_at < :now - 86400
```

**La retención de 24h no es arbitraria**: la cold-list de la parte 2c consulta
exactamente esa ventana. Borrar más agresivo la desarma.

La **misma** goroutine borra también `admin_grants` vencidos en el mismo tick —
sin retención extra, ahí no hay nada que consultar después del canje:

```sql
DELETE FROM admin_grants WHERE expires_at < :now
```

### b) Purga puntual del backlog

Una vez, en prod, con backup previo de la DB: volar todo `otp_codes` vencido de
golpe. Limpia el rastro del ataque sin esperar a que el reaper lo drene.

Justificación de privacidad además de higiene: ahí hay direcciones de terceros que
nunca pidieron nada. Son víctimas del bombing, no usuarios, y no hay razón para
retenerlas.

### c) Revisar `users`

Contar y listar. Cualquiera que el operador no reconozca se elimina con
`HardDeleteUser` — que ya existe y respeta `deletion_log` y las CASCADEs — nunca
con un `DELETE` crudo.

## Parte 4 — Operación y deploy

### Hazard de secuencia

El CD es automático: push a `main` → deploya. Y el fail-fast va a exigir las keys
de Turnstile. **Si se pushea antes de ponerlas en `/etc/mokuji/env`, prod no
arranca.** Las env vars van primero; el binario viejo simplemente las ignora, así
que ponerlas antes no rompe nada.

### Orden

1. Sacar site key + secret de Cloudflare Turnstile para `mokuji.org`.
2. Agregar `TURNSTILE_SITE_KEY` y `TURNSTILE_SECRET_KEY` a `/etc/mokuji/env`.
   Confirmar que `ADMIN_TOKEN` está poblado. Sin restart todavía.
3. Backup de la DB + recoger conteos de `users` y `otp_codes`.
4. Push a `main` → CD deploya v1.0.5. Verificar `curl https://mokuji.org/version`
   y `systemctl status mokuji`.
5. **Entrar con `grant-session`.** Acá se recupera el acceso, todavía sin correo
   funcionando — lo que de paso verifica el break-glass antes de necesitarlo de
   verdad.
6. Purga de `otp_codes`; revisión de `users`.
7. Key nueva de Resend en `SMTP_PASSWORD`, restart, y probar un login real.
8. `limit_req` en nginx como defensa de borde (pendiente desde v1.0.4):

```nginx
limit_req_zone $binary_remote_addr zone=authstart:10m rate=6r/m;

location = /auth/start {
    limit_req zone=authstart burst=3 nodelay;
    limit_req_status 429;
    proxy_pass http://127.0.0.1:8080;
}
```

Rollback en cualquier punto: `mokuji-deploy` deja `mokuji.prev`.

## Hallazgo colateral: la separación de deliverability es falsa

[`docs/estado.md:90`](../../estado.md) documenta que `SMTP_FROM_PRODUCT` separa auth
de product "para deliverability independiente". **No lo hace.**
`auth@mokuji.org` y `releases@mokuji.org` son el mismo dominio, con la misma
alineación SPF/DKIM y la misma reputación. Cuando el atacante quemó la reputación
del OTP, se llevó los digests con él.

La separación real requiere subdominios verificados aparte en Resend
(`auth.mokuji.org` y `mail.mokuji.org`, cada uno con su DKIM).

**Fuera de alcance de este spec**: es trabajo de DNS, no bloquea nada de lo
anterior, y merece su propio minor. Pero la afirmación en `estado.md` debe
corregirse como parte de v1.0.5 para no dejar documentación que miente.

## Fuera de alcance

- Subdominios de sending separados (arriba).
- Webhooks de bounce/complaint de Resend alimentando una lista de supresión.
- Passkeys / WebAuthn (ya en el backlog).
- Persistir rate limits en DB. Los in-memory se resetean en cada deploy, pero la
  cold-list (2c) cubre el caso que importa y es DB-backed.

## Riesgos aceptados

1. **`ADMIN_TOKEN` es un single point of failure** para el bypass de auth. Mitigado
   con 404 en users inexistentes, single-use, TTL de 5 min y logging WARN.
2. **Token de grant en query string** → queda en logs de nginx. Mitigado con
   single-use + TTL corto.
3. **Dependencia de Cloudflare** en el path de login, y fricción de setup para
   self-hosters. Decisión explícita del operador.
4. **Un atacante que resuelva Turnstile** sigue pudiendo generar hasta 24
   correos/día a extraños. Es el techo del bucket de desconocidos, y es un nivel de
   bounces que no mueve la aguja de la reputación.
