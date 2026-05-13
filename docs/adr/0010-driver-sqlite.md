# ADR 0010: Driver de SQLite — modernc.org/sqlite (pure Go)

- **Estado**: Aceptada
- **Fecha**: 2026-05-13

## Contexto

La decisión de usar SQLite (cerrada en el brief inicial) implica elegir un driver de Go. Las opciones reales son dos:

- [`github.com/mattn/go-sqlite3`](https://github.com/mattn/go-sqlite3) — bindings a libsqlite via CGO. Históricamente el driver más usado en Go.
- [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) — SQLite transpilado a Go puro. Sin CGO.

## Decisión

**`modernc.org/sqlite`.**

## Razones

1. **Self-hosters no necesitan toolchain de C** para `go build`. Un usuario en macOS sin Xcode CLI tools, o en una distro Linux mínima sin `gcc`, puede compilar mokuji sin instalar nada extra. Esto baja la barrera de entrada de forma significativa para un proyecto self-hostable.
2. **Cross-compile sin friction**: build en macOS, deploy a Ubuntu Linux. Sin `CGO_ENABLED=0` ni Docker para el build. Para nuestro pipeline (compilo en máquina de desarrollo, scp al VPS) esto es relevante operativamente.
3. **Performance suficiente** para el perfil esperado. modernc es ~10-30% más lento que mattn en benchmarks puros de SQL, pero estamos muy lejos de un workload donde eso importe.
4. **Usado en producción seria**: Tailscale, ent, fly.io y varios proyectos significativos lo usan. No es experimental.

## Alternativas consideradas

- **`github.com/mattn/go-sqlite3`**: descartado por requerir CGO. La performance levemente superior no compensa el costo de barrera de entrada para self-hosters ni la complicación de cross-compile.

## Consecuencias

- Driver registrado bajo el nombre `"sqlite"` (no `"sqlite3"` como en mattn). Cuidado al copiar snippets de internet — la mitad asumen mattn.
- DSN format usa `?_pragma=name(value)` para configurar PRAGMA. Ej: `?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)`.
- `go.mod` requiere Go 1.25+ (impuesto por modernc.org/sqlite v1.50). Sube nuestro baseline efectivo respecto a la nota inicial de "go 1.22" en [ADR 0003](0003-http-framework-stdlib.md) — el spirit de ese ADR ("última stable cuando se arranque la implementación") sigue respetado.

## Regla de evolución

Migrar a `mattn/go-sqlite3` solo si:

- Aparece un bug o limitación específica de modernc que nos bloquee, o
- El perfil de tráfico crece a un nivel donde la diferencia de performance importe (improbable)

El cambio sería: reemplazar el blank import, ajustar DSN, posiblemente ajustar el build pipeline para habilitar CGO.
