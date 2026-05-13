# ADR 0001: Modelo multi-tenant + open source self-hostable

- **Estado**: Aceptada
- **Fecha**: 2026-05-12

## Contexto

mokuji.org es un tracker de manga pensado para uso público. La decisión de cómo materializar "público" es la decisión fundacional del proyecto — condiciona auth, frontend, schema de DB, costo operativo, y modelo de distribución.

Tres modos no excluyentes están sobre la mesa:

- **Multi-tenant**: cualquier persona se registra y mantiene su lista
- **Single-tenant read-only**: solo la lista del operador es pública; otros la ven pero no escriben
- **Self-hostable**: el código es abierto, cualquiera corre su propia instancia

Multi-tenant y single-tenant son excluyentes. Self-hostable es ortogonal a los otros dos.

## Decisión

**Multi-tenant SaaS + open source self-hostable, modelo Miniflux.**

- mokuji.org se opera como instancia pública donde cualquiera firma con email y trackea su lista
- El código vive en repositorio público (Forgejo self-hosted en `git.odin.do/kengru/mokuji`)
- Otros usuarios pueden clonar y correr su propia instancia
- Prioridad transversal del proyecto: **mantener todo lo más simple posible respetando seguridad estándar**

## Razones

1. **El framing original ("cualquier persona puede usarlo") implica multi-tenant.** Single-tenant solo permitiría que otros vean la lista del operador, no que trackeen la propia.
2. **Open source self-hostable se alinea con la filosofía** de file-over-app y ownership del stack. Extiende la opción de ownership a otros usuarios que prefieran no depender de mokuji.org.
3. **Miniflux ya validó este patrón** para una app multi-tenant Go self-hostable similar en perfil de tráfico y operación. No estamos inventando arquitectura nueva.

## Alternativas consideradas

- **Single-tenant read-only**: descartada porque no cumple "cualquier persona puede usarlo" en sentido literal — el mundo solo podría ver la lista del operador.
- **Multi-tenant cerrado (no open source)**: descartada porque rompe la filosofía de ownership del stack y self-hosting.
- **Single-tenant + open source**: defendible y minimalista, pero exige que cada usuario monte su VPS para usar el producto. Demasiada fricción dado el framing público.

## Consecuencias

- Necesidad de **auth real**: signup, sesiones, aislamiento de datos por usuario.
- Necesidad de **transporte de email** desde el principio (verificación, recuperación).
- Necesidad de **prevención de abuso**: rate limiting en signups, captcha si crece.
- Schema de DB con `user_id` en cada tabla de dominio.
- Decisiones de licencia, branding, y separación entre código y configuración deben tomarse pensando en self-hosters externos.
- mokuji.org y mokuji-as-code deben tener identidad clara: el código debe ser portable, los assets de marca deben estar separados del binario.
