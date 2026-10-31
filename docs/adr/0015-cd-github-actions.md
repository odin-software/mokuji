# ADR 0015: CD con GitHub Actions y deploy por SSH con llave restringida

- **Estado**: Aceptada
- **Fecha**: 2026-10-31 (la decisión se implementó en `7e53fec`)

## Contexto

[ADR 0012](0012-cicd-forgejo-runner.md) puso el CD en un runner host-based de
Forgejo corriendo en mimir, con un script de deploy invocado vía `sudo`. El
repo ahora vive también en GitHub (`odin-software/mokuji`, remote `origin`) y
el workflow de Forgejo se eliminó.

## Decisión

Workflow [`.github/workflows/deploy.yml`](../../.github/workflows/deploy.yml):

1. Dispara en `push` a `main` (y a mano con `workflow_dispatch`), ignorando
   cambios que solo tocan markdown (`**.md`), `docs/**` o `LICENSE`.
2. El runner de GitHub (`ubuntu-24.04`, timeout 5 min) corre `go test ./...`
   y, solo si pasa, compila con `make build-linux-amd64`. La versión de Go
   sale de `go.mod` y `actions/setup-go` cachea los módulos.
3. Entrega el binario por SSH a `deploy@mimir.odin.do`, mandándolo por stdin
   al comando `mokuji`. La llave (`DEPLOY_SSH_KEY`) y el `known_hosts` son
   secrets del repo; `StrictHostKeyChecking=yes`.
4. `concurrency` serializa los deploys sin cancelar el que está en curso.

La llave solo puede lanzar el deploy de mokuji (comando forzado en el
servidor). Las migraciones siguen corriendo al arrancar el binario.

## Consecuencias

- **Merge a `main` = deploy a producción.** Igual que antes, pero ahora el
  runner es externo a mimir.
- **Un test roto frena el deploy**: el binario no se compila ni se entrega.
- **Las migraciones se respaldan solas.** El binario aplica las pendientes al
  arrancar, cada una en su transacción, y antes de la primera copia la base
  con `VACUUM INTO` a `<DB_PATH>.pre-migracion-<fecha>.bak` (0600, últimos 3).
  Si el respaldo falla no migra y el proceso no arranca; si una migración
  falla, la transacción hace rollback y la base queda como estaba. Ver
  `internal/migrations`.
- Se pierde la postura "sin servicios externos" de 0012 a cambio de no
  mantener un runner en el servidor de producción. La llave restringida acota
  el daño si el secret se filtra.
- Rollback, backup del binario previo y health check dependen del script
  `mokuji` del lado de mimir, que no vive en este repo: ahí hay que
  confirmarlos.
