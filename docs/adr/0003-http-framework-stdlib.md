# ADR 0003: HTTP framework — stdlib net/http puro

- **Estado**: Aceptada
- **Fecha**: 2026-05-12

## Contexto

Server-rendered con ~15 endpoints estimados ([ADR 0002](0002-frontend-server-rendered.md)). Decisión: routing y middleware con stdlib `net/http` vs `chi` vs framework opinionado (echo, gin, fiber).

## Decisión

**stdlib `net/http` puro.** Sin router externo, sin framework.

- `go.mod` con `go 1.22` como baseline mínimo (por el routing nuevo con method matching y path params)
- Versión objetivo: la última stable cuando se arranque la implementación

## Razones

1. **Go 1.22 cambió el juego**: el `ServeMux` ahora soporta method matching (`mux.HandleFunc("GET /manga/{id}", ...)`), path params, y reglas de precedencia razonables. Antes de 1.22, chi era casi obligatorio. Después, ya no.
2. **Middleware en stdlib es trivial**: patrón `func(http.Handler) http.Handler`. Una middleware completa cabe en 10-20 líneas legibles.
3. **Cero dependencias para algo tan core**: el stdlib de Go no se abandona ni rompe API de forma incompatible. Frameworks tienen ciclo de vida y abandonment risk.
4. **Handlers se ven como cualquier handler Go del mundo**: `func(w http.ResponseWriter, r *http.Request)`. Sin `Context` propio de framework que solo aplica dentro de su ecosistema.

## Alternativas consideradas

- **chi**: la mejor alternativa después de stdlib. Compose con `http.Handler`, da `r.Group()` para aplicar middleware a un subset de rutas, ~3000 líneas mantenidas hace años. Descartada porque para ~15 endpoints con 2 grupos de middleware (público + autenticado), el sub-mux manual en stdlib es 6 líneas extra. No justifica la dependencia.
- **echo, gin**: descartadas porque introducen `Context` propio que rompe interop con middleware del ecosistema estándar. Brillan en APIs JSON grandes, no en este perfil.
- **fiber**: descartada por usar `fasthttp` en vez de `net/http`. Bloquea el ecosistema estándar (`gorilla/csrf`, `justinas/nosurf`, etc.) — es lock-in real al framework.

## Consecuencias

- Vamos a escribir middleware propia para: logging, recovery, request ID, auth, helper de render. Total estimado: ~100 líneas de plumbing visible y auditable.
- Las libs auxiliares que consideramos (sessions, CSRF, hashing) son todas compatibles con stdlib `net/http`.
- Versión mínima en `go.mod` queda en `1.22`. Cuando se arranque a codear, se usa la última stable disponible.

## Regla de evolución

Introducir chi solo si la organización de middleware llega a 4-5 grupos diferentes y el sub-mux manual deja de ser legible. La migración es trivial porque chi compose con `http.Handler` — no se reescribe nada existente.
