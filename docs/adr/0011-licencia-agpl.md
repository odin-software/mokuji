# ADR 0011: Licencia — AGPL-3.0

- **Estado**: Aceptada
- **Fecha**: 2026-05-13

## Contexto

mokuji va a ser código abierto. La elección de licencia condiciona qué pueden hacer terceros con el código y bajo qué obligaciones.

Opciones discutidas:

- **AGPL-3.0** — copyleft con cláusula network: quien corra mokuji como servicio público para otros usuarios debe ofrecer el código (con modificaciones) a esos usuarios
- **MIT** — permisiva; permite uso comercial, cierre del código, sin obligación de devolver
- **Apache 2.0** — permisiva con grant explícito de patentes

## Decisión

**AGPL-3.0.** Archivo `LICENSE` con el texto canónico en root del repo.

## Razones

1. **Alineación con filosofía del proyecto**: ownership y self-hosting genuinos. Si alguien toma mokuji y monta un SaaS cerrado por encima, la licencia obliga a que devuelvan el código.
2. **Coherencia con el modelo de distribución**: ya elegimos en [ADR 0001](0001-modelo-multitenant-open-source.md) ser multi-tenant + self-hostable estilo Miniflux. Miniflux es AGPL-3.0; adoptar la misma postura legal alinea expectativas.
3. **Sin penalización práctica para usuarios reales**: un hobbyist que corre mokuji para sí mismo o para amigos no tiene obligaciones nuevas. Las obligaciones aplican a quien lo opera como servicio público — y en ese caso es justo que el código esté disponible.
4. **Precedente claro**: AGPL-3.0 está bien establecida (GitLab CE, Plausible, Bitwarden Server, Mastodon). No es licencia exótica.

## Alternativas consideradas

- **MIT**: descartada porque permite que un tercero forke mokuji, agregue features, y monte un competidor SaaS cerrado sin obligación de devolver. No alineado con la filosofía del proyecto.
- **Apache 2.0**: descartada por las mismas razones que MIT. El grant de patentes que aporta sobre MIT no es relevante al perfil de este proyecto.

## Consecuencias

- Archivo `LICENSE` con el texto canónico de AGPL-3.0 en el root del repo.
- Cualquier contribución externa se entiende bajo AGPL-3.0 (estándar implícito en repos con `LICENSE`).
- El deploy público en `mokuji.org` debe ofrecer su código fuente. Esto se cumple porque el código vive público en `git.odin.do/kengru/mokuji`.
- Headers SPDX (`// SPDX-License-Identifier: AGPL-3.0-or-later`) podrían agregarse a archivos `.go` para claridad, pero no es necesario legalmente con `LICENSE` en root.

## Regla de evolución

Cambiar de licencia es traumático en proyectos con contribuidores externos: cada contribuidor debe aprobar el cambio o su contribución debe excluirse. Si en el futuro hay necesidad de relicenciar, evaluar el trade-off entre relicenciar la versión actual o forquear con licencia distinta a partir de cierto commit.
