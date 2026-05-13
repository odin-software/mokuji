# ADR 0002: Frontend server-rendered con html/template + vanilla JS

- **Estado**: Aceptada
- **Fecha**: 2026-05-12

## Contexto

Multi-tenant + simple + seguro ([ADR 0001](0001-modelo-multitenant-open-source.md)) implica decidir cómo renderizar la UI. Opciones discutidas:

- `html/template` del stdlib + progressive enhancement (htmx, alpine, o vanilla JS)
- SPA con React o framework similar contra API JSON

## Decisión

**Server-rendered con `html/template` del stdlib + vanilla JS donde sea estrictamente necesario.**

Sin htmx, sin alpine, sin React. La UI base funciona con HTML + forms POST. Se agrega JavaScript solo donde mejore UX caliente de forma significativa (ej: marcar capítulos leídos sin recargar la lista entera).

## Razones

1. **`html/template` del stdlib hace auto-escape consciente del contexto** (HTML body, atributos, JS, URLs). Es uno de los renderers más seguros que existen y está en el stdlib. SPAs requieren cuidado manual en cada `innerHTML` y abren superficie de XSS dinámico.
2. **Un solo proceso, un solo binario, un solo lenguaje.** SPAs introducen toolchain de Node, build pipeline, decisiones sobre bundling y source maps en producción. Encajan mal con minimalismo.
3. **Miniflux validó este patrón** para multi-tenant Go self-hostable similar al nuestro.
4. **Performance por defecto**: first load sin JS bundle, SEO sin esfuerzo, funciona sin JavaScript habilitado.
5. **Ningún flujo del producto justifica SPA.** Listar manga, marcar leído, agregar, buscar, editar nota — todas son operaciones discretas que un POST + redirect resuelve.

## Alternativas consideradas

- **SPA con React (+ API JSON)**: descartada por complejidad operativa, surface area de seguridad mayor, y ningún flujo del producto que la justifique.
- **`html/template` + htmx**: defendible pero prematura. Htmx es un paradigma (hypermedia-driven, fragmentos HTML por endpoint), no solo un script. Para ~15 endpoints es overkill. Se puede introducir después sin reescribir nada — añade atributos a HTML que ya escribimos.
- **Alpine.js**: misma lógica. Útil si aparece estado reactivo inline complejo, no es necesario hoy.

## Consecuencias

- Plantillas viven en disco como `.tmpl` o `.html` y se compilan al binario con `embed.FS` para deploys simples.
- Cada interacción de mutación es un form POST + redirect con flash messages para feedback.
- Decisión de CSS pendiente, pero el camino simple (CSS plano con variables custom) encaja con el resto del approach.

## Regla de evolución

Introducir htmx solo si la UX caliente justifica más de ~100 líneas de vanilla JS o si aparece un patrón repetido de fragmentos HTML por endpoint. No antes.
