# Architecture Decision Records

Cada ADR documenta una decisión arquitectónica de peso: el contexto en el que se tomó, las razones, las alternativas consideradas, y las consecuencias previstas.

## Convenciones

- Numeradas secuencialmente, sin reordenar.
- **Inmutables** una vez aceptadas. Si una decisión cambia, se escribe ADR nueva que reemplaza la vieja. La vieja se marca como `Reemplazada por NNNN` y se conserva.
- Filenames en kebab-case sin acentos: `NNNN-titulo-corto.md`.
- Estado inicial: `Aceptada`. Otros estados posibles: `Reemplazada por NNNN`, `Obsoleta`.

## Plantilla

[plantilla.md](plantilla.md)

## Índice

| #    | Título                                                                | Estado    | Fecha      |
|------|-----------------------------------------------------------------------|-----------|------------|
| [0001](0001-modelo-multitenant-open-source.md) | Modelo multi-tenant + open source self-hostable      | Aceptada  | 2026-05-12 |
| [0002](0002-frontend-server-rendered.md)       | Frontend server-rendered con `html/template` + JS    | Aceptada  | 2026-05-12 |
| [0003](0003-http-framework-stdlib.md)          | HTTP framework: stdlib `net/http` puro               | Aceptada  | 2026-05-12 |
| [0004](0004-auth-email-otp.md)                 | Autenticación con email OTP codes (sin passwords)    | Aceptada  | 2026-05-12 |
| [0005](0005-sessions-propias.md)               | Sessions: implementación propia con SQLite           | Aceptada  | 2026-05-12 |
| [0006](0006-csrf-propio.md)                    | CSRF: implementación propia con synchronizer token   | Aceptada  | 2026-05-12 |
| [0007](0007-email-transport.md)                | Transporte de email: abstracción SMTP + Resend       | Aceptada  | 2026-05-12 |
| [0008](0008-estructura-del-repo.md)            | Estructura del repo: intermedia con `internal/` por feature | Aceptada  | 2026-05-12 |
| [0009](0009-migration-tooling.md)              | Migration tooling: runner propio en ~60 líneas       | Aceptada  | 2026-05-13 |
| [0010](0010-driver-sqlite.md)                  | Driver de SQLite: `modernc.org/sqlite` (pure Go)     | Aceptada  | 2026-05-13 |
