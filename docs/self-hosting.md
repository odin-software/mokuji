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
| `DB_PATH` | `./data/mokuji.db` | Path al archivo SQLite. Crea el directorio antes; mokuji crea el archivo. |
| `SMTP_HOST` | — | Host SMTP del proveedor de email. |
| `SMTP_PORT` | — | Puerto SMTP. Usa un puerto STARTTLS (587, 2587, 2525). Implícito TLS (`465`) no es soportado por la stdlib de Go. |
| `SMTP_USERNAME` | — | Usuario SMTP. |
| `SMTP_PASSWORD` | — | Password / API key. |
| `SMTP_FROM` | — | Dirección "From" para OTPs. Ej: `mokuji <auth@tudominio.com>`. |

### Obligatorias si expones a internet

| Variable | Default | Notas |
|----------|---------|-------|
| `SECURE_COOKIES` | `false` | `true` cuando hay HTTPS. Activarlo hace fail-fast si faltan los secretos de abajo. |
| `TRUST_PROXY` | `false` | `true` si hay reverse proxy delante. mokuji lee `X-Forwarded-For` para rate limiting y logs. NO actives sin proxy — un cliente podría spoofear el IP. |
| `UNSUBSCRIBE_SECRET` | — | HMAC para firmar links de unsubscribe. Mínimo 32 bytes. Genera con: `openssl rand -base64 32`. Rotarlo invalida los links pendientes. |
| `DELETION_LOG_PEPPER` | — | Pepper para hashear user_id en el audit log de borrado de cuentas. Genera igual que arriba. |
| `TURNSTILE_SITE_KEY` | — | Site key de Cloudflare Turnstile para tu dominio (dashboard de Cloudflare, sección Turnstile). Es el reto anti-bot que se muestra antes de mandar un OTP en `/auth/start` (entró en v1.0.5 tras un incidente de email bombing). |
| `TURNSTILE_SECRET_KEY` | — | Secret key del mismo widget de Turnstile. Con `TURNSTILE_SITE_KEY` vacía en dev no se muestra ningún reto — útil si corres mokuji privado sin exponerlo. |

### Opcionales por feature

| Variable | Default | Activa |
|----------|---------|--------|
| `SMTP_FROM_PRODUCT` | comparte `SMTP_FROM` | "From" separado para emails de producto (digest). Útil para que la deliverability de auth y producto sean independientes. |
| `ADMIN_TOKEN` | (vacío = endpoints deshabilitados) | Bearer token para `POST /admin/trigger-digest` y `GET /admin/sources-health`. |
| `FEEDBACK_EMAIL_TO` | (vacío = feature desactivado) | Destino del form `/feedback` in-app. Solo users autenticados; rate-limit 5/hora por user_id. Si vacío, la ruta y el link en el footer no existen. |
| `MANGADEX_ENABLED` | `true` | Source primaria. Apaga con `false` si prefieres solo Asura/Comick. **Si las 3 sources están apagadas**, mokuji arranca pero el worker no se construye — la biblioteca funciona como catálogo manual sin tracking automático. |
| `ASURA_ENABLED` | `true` | Source [Asura Scans](https://asurascans.com). Zona gris legal (scanlation no oficial); bájalo a `false` si prefieres solo MangaDex. |
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

1. **`cp` con app parada**. Lo más simple. Sirve si la instancia tolera
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
tenga una garantía formal de reversibilidad.

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
puertos alternativos similares; revisa su documentación.

Nota relacionada: la stdlib de Go (`net/smtp`) no soporta TLS implícito en
`:465`, solo STARTTLS. Conectarte a `:465` cuelga hasta timeout. Usa
siempre puertos STARTTLS.

## Update path

1. Backup del DB (ver "Persistencia").
2. Baja el binario nuevo.
3. Reemplaza el binario actual.
4. Reinicia el proceso supervisor.
5. Verifica `/version` devuelve la versión esperada.

```bash
curl https://tu-instancia/version
```

Las migraciones corren solas durante el startup. Si una falla, mokuji loggea
el error y aborta — revisa los logs y restaura el backup del DB antes de
intentar de nuevo.

## Troubleshooting

- **Logs**: van a stdout del proceso. Con systemd: `journalctl -u mokuji
  -f`. Con docker: `docker compose logs -f mokuji`. Sin supervisor:
  redirige `> mokuji.log 2>&1` al arrancar.
- **DB ubicación**: el valor de `DB_PATH`. Verifica permisos del directorio
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
