# Política de seguridad

## Reportar una vulnerabilidad

Si encontraste una vulnerabilidad en mokuji, contacta directamente a **support@odin.do**. No abras un issue público.

Si puedes, incluye:

- Descripción del problema y su impacto potencial
- Pasos para reproducir
- Versión afectada (idealmente el commit hash de tu build)
- Cualquier mitigación temporal que conozcas

## Compromiso de respuesta

- Respuesta inicial: dentro de 7 días.
- Después de confirmar y mitigar el problema, coordinaremos disclosure responsable contigo.
- Crédito al reporter en el changelog si lo deseas.

## Versiones soportadas

mokuji está en desarrollo activo. La única versión soportada es `main` del repositorio. No hay releases formales todavía.

## Threat model

La arquitectura de seguridad de mokuji está documentada en los Architecture Decision Records bajo [`docs/adr/`](docs/adr/). Los más relevantes para auth y datos de usuario:

- [ADR 0004](docs/adr/0004-auth-email-otp.md) — autenticación con email OTP
- [ADR 0005](docs/adr/0005-sessions-propias.md) — sessions server-side
- [ADR 0006](docs/adr/0006-csrf-propio.md) — protección CSRF
- [ADR 0007](docs/adr/0007-email-transport.md) — transporte de email

Si tu reporte se relaciona con alguno de estos modelos, referenciar el ADR relevante ayuda a contextualizar el issue.

## Out of scope

- Ataques de denegación de servicio (DoS) por volumen — el rate limiting actual está en `internal/ratelimit`; reportes específicos sobre evasión del rate limit **sí** son in-scope.
- Vulnerabilidades en dependencias upstream — reportar al proyecto correspondiente.
- Problemas que requieran acceso físico al servidor o credenciales de admin.
- Self-XSS, clickjacking en páginas que no manejan estado autenticado.
