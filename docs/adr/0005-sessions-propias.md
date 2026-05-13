# ADR 0005: Sessions — implementación propia con SQLite

- **Estado**: Aceptada
- **Fecha**: 2026-05-12

## Contexto

[ADR 0004](0004-auth-email-otp.md) define email OTP como mecanismo de autenticación. Después de validar el código, el usuario debe quedar autenticado durante una ventana larga sin tener que pedir un código nuevo en cada request. Necesitamos un mecanismo de session que:

- Sea revocable instantáneamente (logout, "cerrar todas las sesiones")
- Tolere el modelo PWA iOS donde no podemos asumir state compartido entre contextos
- Sea legible top-to-bottom sin abstracciones de framework

Dos opciones discutidas:

- **Lib `alexedwards/scs`**: lib Go bien establecida para sessions con storage pluggable y API genérica de Get/Put
- **Propio en ~100 líneas**: package `session` SQLite-only, sin abstracciones de framework

## Decisión

**Implementación propia.** API mínima:

```
session.Manager
  ├─ New(db) *Manager
  ├─ Create(w, userID) error
  ├─ Load(r) (*Session, error)
  ├─ Destroy(w, r) error
  ├─ CleanupExpired(ctx) error
  └─ Middleware(next) http.Handler

session.FromContext(ctx) (*Session, bool)
```

Parámetros confirmados:

- **Session ID**: 256 bits de `crypto/rand`, codificado en base64url (43 chars)
- **Storage server-side**: el **hash SHA-256 del ID**, no el plaintext (defense-in-depth contra DB leak)
- **Sliding window**: 90 días desde último uso. Optimización: solo `UPDATE` la fila si pasó más de 1 hora desde `last_used_at` (evita una escritura por request)
- **Regeneración del ID al autenticar**: prevención de session fixation. La sesión post-OTP usa un ID distinto al pre-OTP si lo hubiera
- **Cookie**: `Name=mokuji_session`, `Path=/`, `Max-Age=7776000` (90 días), `Secure`, `HttpOnly`, `SameSite=Lax`

Schema de DB:

```sql
CREATE TABLE sessions (
    id_hash      TEXT    PRIMARY KEY,             -- SHA-256 hex del session ID
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    user_agent   TEXT,
    ip_address   TEXT
);
CREATE INDEX idx_sessions_user    ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
```

Cleanup de sesiones expiradas: goroutine con `time.Ticker` cada hora, ejecuta `DELETE FROM sessions WHERE expires_at < ?`.

## Razones

1. **El mecanismo entero cabe en ~100 líneas legibles**, alineado con el principio de comprensión sobre conveniencia para primitivas de seguridad.
2. **Hashear el session ID server-side** (que scs no hace por default) agrega una capa concreta de defense-in-depth contra leaks de DB.
3. **Cero dependencias** en algo tan core como auth state. Solo stdlib + `crypto/rand` + `crypto/sha256` + `database/sql`.
4. **No necesitamos las features extra de scs**: solo un backend de storage (SQLite), no necesitamos guardar datos arbitrarios por sesión. Flash messages se resolverán con un mecanismo separado y minimal cuando aparezca el primer redirect que los requiera.

## Alternativas consideradas

- **`alexedwards/scs`**: descartada porque aporta ~3000 LOC + dependencia para resolver problemas que no tenemos (múltiples storage backends, API genérica de Put/Get) y no hace hashing de session IDs server-side. Es buena lib, no ganamos lo suficiente para meterla.
- **`gorilla/sessions`**: descartada por estar en peor estado de mantenimiento y por usar cookies firmadas (stateless con datos en el cookie) como modelo principal, que no es lo que queremos.
- **JWT stateless**: descartado implícitamente al elegir sessions stateful — necesitamos revocación trivial y full control server-side, no escala global.

## Consecuencias

- Vamos a escribir y mantener ~100 líneas de código de seguridad sensible. Trade-off aceptado por comprensión.
- Cleanup de expirados es responsabilidad nuestra (goroutine con ticker), no de una lib.
- Si en el futuro necesitamos persistir datos arbitrarios en sesión, agregamos una tabla `session_data` o columna `data BLOB` cuando aparezca el caso de uso.
- Flash messages NO se resuelven con sessions — pendiente de diseño cuando aparezca el primer redirect que requiera feedback al usuario.

## Regla de evolución

Migrar a scs solo si:

- Aparece necesidad real de múltiples backends de storage (improbable en self-hosted SQLite-only), o
- La cantidad de datos por sesión crece a algo no trivial donde la API de scs aporte ergonomía clara

El código nuestro está aislado en un package — la migración sería local, no invasiva.
