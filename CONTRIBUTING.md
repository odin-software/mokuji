# Contribuir a mokuji

Bienvenido. mokuji es un proyecto chico y abierto, mantenido por una sola
persona; contribuciones son bienvenidas y se revisan a paso humano.

## Empezar

```bash
git clone https://github.com/odin-software/mokuji.git
cd mokuji
cp .env.example .env       # ajusta lo que necesites
make compose-up            # levanta mailpit local en localhost:8025
make dev                   # corre el binario con live reload (requiere `air`)
```

Si no tienes `air` instalado, `make run` corre el binario una sola vez.

Para correr migraciones contra la DB local sin levantar el server:

```bash
make migrate
```

## Tests

```bash
make test
```

Todos los tests son `go test` puro. No hay frameworks extra. La suite corre
en menos de un minuto en hardware decente.

Cuando agregues features nuevos, agrega tests. Cuando arregles un bug,
agrega el test que falla **antes** del fix (regression test).

## Estructura del repo

- `main.go` — composition root: lee env vars, wirea handlers y workers.
- `internal/<package>/` — un paquete por feature. Los nombres son cortos
  y descriptivos (`auth`, `library`, `releases`, etc.).
- `internal/server/` — HTTP routing, middleware, security headers.
- `migrations/` — `.sql` numerados, up-only.
- `web/templates/` — `html/template`. Server-rendered, sin frameworks.
- `web/static/` — JS y assets servidos en `/static/`.
- `web/emails/` — templates de emails outbound.
- `docs/` — documentación viva.
- `docs/adr/` — decisiones arquitectónicas inmutables.

## Commits

- Subject corto, español, lowercase, sin punto final.
- Cuerpo si la motivación no es obvia del diff.
- Co-author trailer si fue trabajo en pair.

Ejemplo:

```
seo: robots.txt + sitemap.xml + llms.txt en el root

Tres endpoints estáticos servidos en la raíz vía mountDiscoverability.
robots.txt es allow-all + sitemap pointer. sitemap.xml lista solo `/` por
ahora (no hay rutas públicas indexables más allá de la landing). llms.txt
sigue la propuesta de llmstxt.org para que LLMs que indexen el sitio
entiendan el contexto del proyecto.
```

## ADRs

Las decisiones arquitectónicas con tradeoff real van en `docs/adr/` como
markdown numerado. **Una ADR no es un changelog ni un design doc** — es la
fotografía de una decisión que se tomó, las alternativas que se evaluaron,
y por qué quedó así.

Haz ADR cuando:

- Hay más de una opción razonable y elegimos una.
- La decisión es difícil de cambiar después (deuda técnica si no se
  documenta).
- El reasoning no es obvio desde el código.

No hagas ADR cuando:

- Es la única opción posible (no hay tradeoff).
- Es un detalle de implementación, no de arquitectura.

ADRs son inmutables una vez aceptadas. Si una decisión cambia, se escribe
una ADR nueva que la reemplaza y se marca la vieja como
`Reemplazada por NNNN`.

## Pull requests

PRs van contra `main` directo. Por ser un repo chico, no hay ramas de
release ni gitflow.

- Para cambios chicos (typo, bugfix obvio, mejora de copy): PR directo,
  sin issue previo.
- Para cambios grandes (feature nuevo, refactor, dependencia nueva): abre
  una discusión en un issue **primero**. Evita que escribas algo y después
  haya que descartarlo por scope.
- Mantén el PR enfocado: si terminas refactoreando algo de paso, sepáralo
  en commit aparte o en otro PR.

CI: cada push corre el deploy a producción cuando el branch es `main`. Los
tests todavía no corren en CI (pendiente). Por ahora, **corre `make test`
local antes de abrir el PR**.

## Voz

Toda la copy del producto está en español **dominicano** — tuteo, nunca
voseo. Verbos en 2ª persona usan `tú` (`tienes` no `tenés`, `lees` no
`leés`, `eliges` no `elegís`). Imperativos van con acento penúltimo
(`agrega` no `agregá`, `prueba` no `probá`, `vuelve` no `volvé`).

Más allá del dialecto:

- Tono cálido y específico, no corporativo. "todavía no hay capítulos
  nuevos" gana sobre "no se encontraron registros".
- Errores accionables. "demasiados intentos en pocos minutos. espera un
  poco y vuelve a intentar" gana sobre "rate limit exceeded".
- Sin jerga innecesaria. Si una palabra técnica tiene equivalente claro en
  español, úsala (ej. `mostrar`, no `renderizar`).
- mokuji se escribe **siempre minúscula**, incluso al inicio de oración.
  El wordmark visual es `目次` cuando puede ir.

## Reportes de seguridad

No abras issues públicos para vulnerabilidades. Mira [SECURITY.md](SECURITY.md)
para el flow privado.

## Code of conduct

Sé amable. mokuji es un proyecto chico y la conversación en issues y PRs es
el primer contacto con cualquiera que llegue. Discrepa con respeto, asume
buena fe, y cuando una conversación se ponga difícil, baja un cambio.

## Licencia

Al contribuir, aceptas que tu contribución se licencia bajo
[AGPL-3.0](LICENSE), la misma licencia del proyecto.
