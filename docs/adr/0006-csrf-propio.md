# ADR 0006: CSRF — implementación propia con synchronizer token por sesión

- **Estado**: Aceptada
- **Fecha**: 2026-05-12

## Contexto

La cookie de sesión definida en [ADR 0005](0005-sessions-propias.md) usa `SameSite=Lax`, que bloquea CSRF básico en navegadores modernos. Sin embargo, hay vectores que SameSite no cubre:

- Navegadores viejos sin soporte de SameSite
- Subdominios hermanos considerados "same-site"
- Bugs específicos de configuraciones de browser
- GET cross-site con cookie en navegación top-level (si algún endpoint GET mutara estado por error)

CSRF tokens en pattern synchronizer son defensa primaria. SameSite es la red.

Opciones discutidas:

- `justinas/nosurf` — lib con rotación por request, double-submit, ~600 LOC
- `gorilla/csrf` — similar, ~1000 LOC, 1 dep transitiva
- Propio en ~50 líneas

## Decisión

**Implementación propia.** Synchronizer token atado a sesión, sin rotación por request.

Mecanismo:

1. Al crear una sesión (en `session.Create`), generar también un `csrf_token` random de 256 bits con `crypto/rand`.
2. Guardar el token en la fila de la sesión (nueva columna `csrf_token`, ver schema abajo).
3. Helper de template `{{ csrfInput }}` rinde `<input type="hidden" name="csrf_token" value="...">`.
4. Middleware CSRF aplicado globalmente:
   - Método safe (GET, HEAD, OPTIONS) → pasa
   - Sin sesión → pasa. Endpoints anónimos (`/auth/start`, `/auth/verify`) no necesitan CSRF porque no hay credenciales autenticadas que un atacante pueda abusar; rate limiting cubre el riesgo de spam.
   - Con sesión y método state-changing (POST, PUT, DELETE, PATCH) → extraer token del form o header `X-CSRF-Token`, comparar contra `session.CSRFToken` con `crypto/subtle.ConstantTimeCompare`. Mismatch → 403.
5. Regenerar token cuando se regenera la sesión (login, logout explícito, "cerrar todas las sesiones").

Cambio al schema (sobre la tabla `sessions` definida en ADR 0005):

```sql
ALTER TABLE sessions ADD COLUMN csrf_token TEXT NOT NULL DEFAULT '';
```

## Razones

1. **El mecanismo cabe en ~50 líneas** auditable y obvio top-to-bottom.
2. **Cero dependencias adicionales.**
3. **Token por sesión (no por request)** es práctica estándar (Rails, Django) y es suficiente para nuestro threat model. Rotación por request protege contra leak del token via referer/history — irrelevante para nuestro perfil.
4. **Synchronizer atado a sesión es natural** dado que ya tenemos sesiones server-side. El token vive en la misma fila, comparte ciclo de vida.
5. **Endpoints anónimos no necesitan CSRF**: simplificación legítima porque nuestros únicos POSTs anónimos son los del flow OTP y no tienen credenciales autenticadas que abusar.

## Alternativas consideradas

- **`justinas/nosurf`**: descartada. Su valor principal — funciona sin sesión, rota por request — no aplica a nuestro modelo donde ya tenemos sesiones server-side y donde los endpoints anónimos no necesitan CSRF.
- **`gorilla/csrf`**: descartada por las mismas razones, más LOC y una dependencia transitiva.
- **Double-submit cookie**: descartada por innecesaria — la sesión ya nos da donde guardar el token.

## Consecuencias

- Mantenemos ~50 líneas de código de seguridad sensible, integrado al package de sesiones.
- El helper de template `{{ csrfInput }}` debe registrarse en el `FuncMap` global. Asegurar que cualquier form POST autenticado lo invoque (idealmente verificable con un test que rastree templates con forms sin el helper).
- Si en el futuro necesitamos CSRF en endpoints anónimos, agregamos un mecanismo adicional de double-submit cookie sin tocar el flow autenticado.

## Regla de evolución

Migrar a `nosurf` o `gorilla/csrf` solo si:

- Aparece necesidad real de CSRF en endpoints anónimos, o
- Identificamos un threat específico que requiera rotación de token por request (improbable a nuestro perfil)
