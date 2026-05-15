# ADR 0007: Transporte de email — abstracción SMTP, Resend default, plain-text inicial

- **Estado**: Aceptada
- **Fecha**: 2026-05-12

## Contexto

[ADR 0004](0004-auth-email-otp.md) requiere transporte de email confiable desde día 1 para entregar OTPs en la ruta crítica del login.

Self-hosting de SMTP propio (Postfix/exim) es operativamente costoso para transaccional:

- DigitalOcean bloquea outbound port 25 por default; el desbloqueo requiere ticket y no siempre se aprueba
- IPs nuevas tienen reputación cero con Gmail, Outlook, iCloud; "warming" toma semanas o meses
- SPF, DKIM, DMARC, reverse DNS, RFC 5322 compliance son configuración delicada que hay que mantener correcta
- Bounces y abuse complaints requieren monitoreo activo
- OTP está en ruta crítica del login: cualquier fricción de SMTP se traduce en fricción de UX

Este es el caso explícito de "self-hosted donde aporta valor, comercial donde es claramente mejor".

## Decisión

**Abstracción sobre SMTP estándar; default Resend para mokuji.org.** Email inicial **plain-text únicamente**.

Implementación:

- Cliente de email habla SMTP plano (stdlib `net/smtp`), no SDK específico de provider
- Configuración por env vars: `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`
- mokuji.org configura Resend
- Self-hosters configuran lo que prefieran (SMTP propio si saben lo que hacen, Postmark, SES, Mailgun, etc.)
- **Desarrollo local**: [Mailpit](https://github.com/axllent/mailpit) corre en `compose.yaml` y captura todo el SMTP en localhost:1025 con UI web en localhost:8025 para inspeccionar los emails enviados durante dev y testing. `.env.example` apunta a Mailpit por default.
- Plantilla del OTP vive en `embed.FS` como `.txt`
- Sin logging server-side de emails enviados — el dashboard del provider tiene la misma info con eventos de delivery/bounce/complaint
- Rate limiting de `/auth/start` se manejará aparte cuando se implemente (counter en memoria o tabla pequeña, decisión menor en implementación)

## Razones

1. **Self-hosted SMTP es exactamente el caso "comercial donde es claramente mejor"**. La filosofía del proyecto lo deja explícito.
2. **Abstracción SMTP estándar evita lock-in a un provider**: self-hosters tienen flexibilidad total, mokuji.org puede cambiar provider sin redeploy de código.
3. **Resend tiene la mejor DX del grupo** para el perfil esperado. Free tier (3k/mes) cubre orden de magnitud por arriba del uso real estimado a corto plazo. Pricing predecible si crecemos.
4. **Plain-text inicial** funciona en cualquier cliente (incluyendo terminal-based), evita batallas de CSS-in-email, mejora auto-fill de OTP en iOS/Android, y se distingue de los phishing emails que típicamente son HTML pesado.
5. **No loguear emails en DB** porque el dashboard del provider mantiene la información de delivery mejor que nosotros, con eventos en tiempo real.

## Alternativas consideradas

- **SMTP propio en mimir**: descartado por razones operativas (port 25, IP reputation, mantenimiento DKIM/SPF/DMARC, riesgo en ruta crítica de login).
- **Postmark**: defendible, ligeramente mejor deliverability histórica. Descartado por DX inferior y diferencia marginal de deliverability a nuestra escala.
- **Amazon SES**: defendible si optimizáramos costo a escala. Descartado por setup más torpe (sandbox approval, IAM) y porque el costo no es factor relevante a esta escala.
- **SDK específico de Resend (HTTP API)**: descartado porque introduce lock-in y resuelve problemas (templating, manejo de eventos) que no necesitamos hoy.
- **HTML email desde día 1**: descartado por ahora. Se planea cuando exista identidad visual del producto.

## Consecuencias

- mokuji.org necesita cuenta en Resend (free tier inicial) y configuración de DKIM/SPF en el DNS de `mokuji.org`
- El README del repo debe documentar las env vars de SMTP para self-hosters
- La plantilla `.txt` del OTP vive en `embed.FS`, se renderiza con stdlib `text/template`
- Cuando se diseñe la marca y se agreguen HTML emails, el `Send` aceptará también un `htmlBody` opcional y el email saldrá como `multipart/alternative` (text + html)

## Regla de evolución

Migrar a HTML emails (manteniendo plain-text como alternative del multipart) cuando:

- Exista identidad visual del producto (logo, paleta, tipografía)
- Se quiera incluir contenido más rico en emails (resumen de actividad, recomendaciones, etc.)

Cambiar de Resend a otro provider solo si:

- Resend cambia pricing o políticas de forma que rompa el modelo, o
- Aparecen problemas de deliverability concretos contra los inboxes de los usuarios

## Nota — Fase 2 (2026-05): product emails multipart

La fase 2 introduce el **digest semanal de releases** ([ADR 0013](0013-fase-2-release-tracking.md) §6). Ese tipo de email requiere headers extra (`List-Unsubscribe`, `List-Unsubscribe-Post` para RFC 8058 one-click) y se beneficia de una versión HTML básica para que los clientes con preview muestren formato razonable.

Decisión derivada, sin abrir un ADR nuevo porque cumple la regla de evolución de arriba:

- **Auth emails (OTP) siguen siendo plain-text via `Client.Send`**. Sin cambios. El auto-fill de iOS/Android y la distinción contra phishing pesan más que la estética acá.
- **Product emails (digest, eventualmente notificaciones) usan `Client.SendMessage`**, que arma `multipart/alternative` con plain + HTML básico y permite `ExtraHeaders`. El HTML no tiene branding aún — es solo formato mínimo (lista, links).
- Ambos métodos comparten el mismo cliente SMTP y la misma config; lo único que cambia es el formato del mensaje.
