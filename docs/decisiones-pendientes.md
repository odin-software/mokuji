# Decisiones pendientes

Lista de decisiones arquitectónicas aún abiertas, en orden tentativo de discusión. Se va recortando a medida que se cierran ADRs.

## Marca / identidad visual

Pendiente. Una vez decidida condiciona:

- HTML email templates (alternative al plain-text actual — ver [ADR 0007](adr/0007-email-transport.md))
- CSS / theming de la app
- Favicon, OG images, iconos de PWA, manifest

## CSS

Plano + variables custom vs framework (Tailwind, etc.). Probablemente plano para empezar, evaluar cuando duela. Depende parcialmente de marca.

## Flash messages

No se resuelven dentro del package de sessions ([ADR 0005](adr/0005-sessions-propias.md)). Diseño pendiente para cuando aparezca el primer redirect que requiera feedback al usuario (probablemente al primer form de mutación). Patrón candidato: tabla `flash_messages` o cookie firmada de corta duración.

## Licencia

Para el repo público. Probablemente **AGPL-3.0** (copyleft fuerte, alineado con filosofía de software self-hostable) o **MIT** (más permisivo, mayor adopción). A discutir antes del primer release.

## Pendientes de explicación (no son decisiones, son aprendizaje)

- **Passkeys / WebAuthn**: cómo se implementaría aunque no se haga ahora. Cubrir registration vs authentication ceremony, origin-binding criptográfico, almacenamiento server-side de public keys, recuperación multi-dispositivo, fallback path si el usuario pierde acceso a todos sus passkeys.
