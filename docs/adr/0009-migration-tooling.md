# ADR 0009: Migration tooling — runner propio en ~60 líneas

- **Estado**: Aceptada
- **Fecha**: 2026-05-13

## Contexto

Necesitamos un mecanismo para aplicar cambios al schema de la DB en el tiempo. Restricciones del proyecto:

- DB es SQLite (decisión cerrada en el brief inicial)
- App es single-binary, single-process en el VPS
- Embeber migraciones al binario elimina la posibilidad de "olvidar archivos" en el deploy

Opciones discutidas:

- [`pressly/goose`](https://github.com/pressly/goose) — ~3000 LOC, soporta SQL y Go-code migrations, múltiples DBs
- [`golang-migrate/migrate`](https://github.com/golang-migrate/migrate) — ~4000 LOC, CLI-first, historial de bugs con locking en SQLite
- Custom — ~60 LOC, solo SQL, solo SQLite, solo up

## Decisión

**Runner propio en `internal/migrations`.** Solo migraciones up. Sin down.

Mecanismo:

1. Tabla `schema_migrations(version TEXT PRIMARY KEY, applied_at INTEGER)` registra qué archivos se aplicaron.
2. SQL files viven en `migrations/` a nivel root del repo ([ADR 0008](0008-estructura-del-repo.md)), con convención `NNNN_descripcion.sql` (`0001_users_and_sessions.sql`, etc.).
3. Archivos embebidos al binario con `//go:embed migrations/*.sql` declarado en `main.go`. El runner recibe el `fs.FS` por parámetro (desacopla la lógica del embed).
4. `migrations.Run(db, fs)` corre al startup: lee archivos ordenados por nombre, aplica los que no estén en `schema_migrations`, cada uno dentro de su propia transacción.
5. Flag `-migrate-only` en el binario permite correr solo migraciones y salir, útil para hooks de deploy y para tests.

API mínima:

```go
package migrations

func Run(db *sql.DB, src fs.FS) (applied int, err error)
```

## Razones

1. **El runner entero cabe en ~60 líneas** auditable top-to-bottom. Migrations es operacional pero corre en el momento más sensible del deploy — querer entender cada decisión que toma esa lógica es legítimo.
2. **Cero dependencias** para algo que ejecuta DDL al startup.
3. **No necesitamos down migrations** (justificación abajo).
4. **No necesitamos múltiples backends** — SQLite-only es nuestra realidad.
5. **No necesitamos Go-code migrations** — DDL en SQL es suficiente para nuestros casos.
6. **`golang-migrate` específicamente tiene historial de bugs con locking en SQLite**, lo cual lo descalifica aún si fuera la lib que prefiriéramos.

### Por qué no down migrations

1. En producción rara vez se corren correctamente. La práctica habitual ante una migración mala es "restaurar backup + escribir fix-forward", no correr down.
2. Doblan código y superficie de bugs sin ganancia clara.
3. Litestream + SQLite (ruta futura en brief inicial) nos da rollback granular más confiable que un down script.
4. Para iterar en local: borrar el archivo SQLite y re-correr todas las migraciones es trivial.

## Alternativas consideradas

- **`pressly/goose`**: la mejor lib si fuéramos a usar una. Descartada porque todo lo que aporta sobre custom (Go-code migrations, múltiples backends, CLI standalone) son cosas que decidimos no necesitar.
- **`golang-migrate/migrate`**: descartada por bugs históricos con locking en SQLite.
- **Down migrations en custom**: descartadas por las razones de arriba.

## Consecuencias

- Migraciones corren automáticamente al startup del binario. Self-hosters no necesitan correr un comando aparte para migrar.
- Schema changes son inevitables al deploy — si el binario nuevo tiene una migración nueva, se aplica al arrancar. Deseable para single-binary deploys.
- Flag `-migrate-only` para casos donde se quiere migrar sin servir tráfico (deploy pipelines más estrictos, debugging).
- Filenames numerados secuencialmente. Cuando dos desarrolladores escriben migraciones en paralelo y chocan en número (ej: ambos crean `0007_*.sql`), uno tiene que renumerar antes de mergear. Trade-off conocido del approach numerado vs timestamp.

## Regla de evolución

Migrar a `goose` solo si:

- Aparece necesidad real de soportar múltiples backends (improbable — somos SQLite-only por filosofía), o
- Necesitamos Go-code migrations para transformaciones complejas de datos (improbable a nuestra escala)

Agregar down migrations solo si aparece un caso real donde forward-fix no funciona.
