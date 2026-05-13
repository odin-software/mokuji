# ADR 0008: Estructura del repositorio

- **Estado**: Aceptada
- **Fecha**: 2026-05-12

## Contexto

Antes de arrancar a codear necesitamos decidir cómo se organizan los archivos en el repo. Tres approaches discutidos:

- **Standard Go Project Layout** ([golang-standards/project-layout](https://github.com/golang-standards/project-layout)): proyecto comunitario popular pero no oficial, con `cmd/`, `pkg/`, `api/`, `internal/`, `build/`, `deployments/`, `scripts/`, `test/`, etc.
- **Flat**: un solo paquete `mokuji` con todos los archivos `.go` en root
- **Intermedia con `internal/` por feature**: `main.go` en root, `internal/` con paquetes por dominio (auth, library, user, etc.), sin `cmd/` ni `pkg/`

Adicionalmente, requerimiento explícito: **todo el sistema debe poder montarse y probarse en local antes de pushear**. Esto implica artifacts de desarrollo local (`compose.yaml`, `Makefile`, `.env.example`) en el repo desde día 1.

## Decisión

**Layout intermedio con `internal/` por feature.**

```
mokuji/
├── README.md
├── LICENSE                     # cuando se decida
├── go.mod
├── go.sum
├── main.go                     # entry point pequeño, solo wiring
├── .env.example                # template de env vars con defaults locales
├── .gitignore
├── compose.yaml                # servicios locales: mailpit + lo que aparezca
├── Makefile                    # tareas comunes: dev, test, build, migrate
├── docs/
│   ├── README.md
│   ├── decisiones-pendientes.md
│   └── adr/
├── migrations/                 # archivos SQL numerados
├── web/
│   ├── templates/              # .html para páginas
│   ├── static/                 # css, js, manifest, icons
│   └── emails/                 # plantillas de email
├── internal/
│   ├── server/                 # http server, routing, middleware composition
│   ├── session/                # session manager (ADR 0005)
│   ├── csrf/                   # csrf middleware (ADR 0006)
│   ├── email/                  # smtp client (ADR 0007)
│   ├── auth/                   # otp flow handlers
│   ├── user/                   # user domain
│   └── library/                # manga library domain
└── deploy/                     # artifacts de producción
    ├── mokuji.service          # systemd unit
    └── nginx.conf              # template de site config
```

Convenciones:

- Tests viven alongside como `_test.go` (Go default). E2E tests en `e2e/` a nivel root cuando se diseñen.
- `internal/` enforced por el compilador de Go: nada fuera del módulo puede importar esos paquetes.
- Paquetes en `internal/` organizados **por feature** (auth, library, user, etc.), no por capa (handlers, services, repository).
- `main.go` en root, no `cmd/mokuji/main.go`, porque hay un solo binario.

## Razones

1. **Standard Project Layout es cargo culting**. Russ Cox lo declaró explícitamente no oficial. Tiene sentido para proyectos grandes con múltiples binarios y contratos formales. Para una app web simple es ceremonial.
2. **Flat (single package) no escala** más allá de ~5 archivos porque todos los nombres viven en el mismo namespace.
3. **Por feature > por capa** para apps small-to-medium: cada paquete es autocontenido (handlers + queries + tipos del dominio en el mismo lugar), agregar un feature toca un paquete en vez de cuatro. Encaja con la filosofía de Go de paquetes con propósito claro.
4. **`main.go` en root** porque un solo binario no necesita `cmd/`. Si aparece un segundo, refactor de minutos.
5. **`compose.yaml` + `Makefile` + `.env.example` en root** porque local-first requiere que `make dev` o `docker compose up` "just work" desde clone.

## Alternativas consideradas

- **Standard Go Project Layout**: descartada por cargo culting y por agregar 10+ directorios que no necesitamos.
- **Flat (`package mokuji` con todo en root)**: descartada por colisión de namespace y dificultad para enforcing boundaries via `internal/`.
- **Por capa** (`internal/handlers`, `internal/services`, `internal/repository`): descartada por dispersar features a través de 4 paquetes y hacer cambios más invasivos.
- **`cmd/mokuji/main.go`**: descartada porque tenemos un solo binario. Si aparecen más, se migra.
- **`justfile` / `mage` en vez de `Makefile`**: descartadas porque `make` está en todos los Unix por default; `just` requiere instalación adicional, `mage` es overkill para shell commands.

## Consecuencias

- `main.go` debe quedar **chico** (solo wiring): leer config, abrir DB, correr migraciones, montar router, arrancar servidor. La lógica vive en `internal/`.
- `internal/server` es responsable de routing y middleware composition. Dependencias entre paquetes: `server` importa todos los otros; los otros no se importan entre sí salvo a través de interfaces explícitas cuando sean necesarias.
- Cuando aparezca un segundo binario (admin CLI, migration runner standalone), refactorizar a `cmd/server/main.go` + `cmd/admin/main.go`. Refactor de minutos.
- `web/static` puede crecer; si llega a ser pesado, considerar cache busting (decisión futura).

## Regla de evolución

- Agregar `cmd/` cuando exista un segundo binario.
- Migrar a `pkg/` solo si decidimos exponer alguno de los paquetes como librería reutilizable (improbable — mokuji es app).
- Romper paquetes grandes en sub-paquetes cuando un paquete supere ~5-7 archivos `.go` o ~1500 líneas. No antes — sub-paquetear prematuramente es peor que tener archivos grandes.
