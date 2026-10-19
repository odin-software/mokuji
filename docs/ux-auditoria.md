# Auditoría de UX — 2026-10-19

Borrador para discutir. Sale de leer templates y handlers (v1.0.8) y de un
recorrido en local con una cuenta nueva (`ux-audit@example.test`): login,
onboarding, agregar Frieren, lista, detalle, home y settings, en desktop
(1024px) y mobile (375px). Lo marcado **[en vivo]** se confirmó en el
navegador.

Disparador: cambiar un manga de "leyendo" a "completado" requiere abrir un
desplegable sin señal visual en `/library`, y la página de detalle del manga
no deja hacerlo.

## Diagnóstico corto

La app tiene **dos modelos de progreso que no se hablan** y **dos lugares
para gestionar un manga que se reparten las acciones a medias**. Casi todo
lo demás es consecuencia de eso o es consistencia menor.

---

## P1 — Modelo de progreso partido (núcleo)

### 1. Dos "capítulos" distintos

| campo | dónde se edita | dónde se muestra | qué mueve |
|---|---|---|---|
| `last_chapter` (texto libre) | desplegable en `/library` ("último capítulo") | lista (`— cap. X`), perfil público, **detalle** (`vas por cap. X`), export | nada |
| `last_read_chapter_number` (cursor) | `/library/entry` ("voy por el capítulo"), "marcar leído", "leer →", link del digest | solo el input del cursor | badges "N nuevos", `/releases`, digest, "ver leídos" |

En el detalle conviven los dos uno encima del otro: el header dice
`vas por cap. 40` (texto manual) y justo debajo el input "voy por el
capítulo" muestra `112` (cursor).

Marcar leído desde `/releases` avanza el cursor pero **no** cambia el
`— cap. X` de la lista ni del perfil público. El user ve dos números que no
cuadran y no sabe cuál es el real. `last_chapter` es un residuo de antes del
release tracking (migración 0003 vs 0008).

### 2. Gestionar un manga está repartido en dos pantallas

- **Desplegable en `/library`**: estado, último capítulo (texto), URL de
  fuente, favorito, notas, borrar.
- **`/library/entry/{id}`**: cursor, capítulos, sources. El estado y el
  favorito se **muestran** pero no se editan.

La página de detalle es el "hogar" natural de un manga (es a donde lleva el
título, los badges y los releases), pero no deja hacer lo más básico.

### 3. El estado nunca reacciona al progreso

- Agregar siempre crea `reading` (`createEntry`), aunque lo que quieras es
  "por leer".
- Marcar leído un capítulo de un manga "por leer" no lo pasa a "leyendo".
- Llegar al último capítulo disponible no sugiere "completado".

No es necesariamente automatizar todo, pero hoy el estado es 100% manual y
vive en la pantalla equivocada.

## P1 — La lista de `/library`

### 4. Fila desplegable sin affordance

`summary { display: flex }` ([layout.html:462](../web/templates/layout.html))
quita el triángulo nativo del `<details>`, así que nada indica que la fila se
abre. Y el elemento más obvio para tocar (el título) navega a otra página.
Resultado: el formulario de edición es prácticamente invisible.

### 5. Guardar te saca de contexto

`update` redirige a `/library` pelado
([library.go:511](../internal/library/library.go)): se pierden `q` y
`filter`, el scroll vuelve arriba y el panel se cierra. Con una lista larga
filtrada por "leyendo", editar dos mangas seguidos es tedioso.

### 6. Agregar es lento y sin feedback

`add` corre `tryMatchSources` dentro del request antes de redirigir.
**[en vivo]** `POST /library/add` tardó **4060 ms** con Frieren, sin
ninguna indicación de que algo pasa; la única confirmación es que el botón
pasa a "ya en tu biblioteca".

### 6b. El detalle miente cuando una source no trae capítulos

**[en vivo]** Frieren hizo match en MangaDex, pero el poll trajo 0
capítulos (los licenciados no están en MangaDex). Resultado en la misma
pantalla:

- sources: `mangadex ⏳ halando capítulos…`. `Pending` es solo
  `matched && 0 chapters`, así que nunca sale de ese estado. El próximo
  check es en 6 h.
- empty state: "sin capítulos todavía. no encontramos releases… prueba
  reintentar". Solo distingue el estado `unmatched` de la entry.
- badge en la lista: `PENDIENTE` sin explicación.

Tres mensajes distintos para la misma situación, y uno dice "esperando"
cuando ya no hay nada que esperar. Además comick quedó en
`⚠ matching incierto` en el primer manga que agregué.

### 6c. "ver leídos" no se puede apagar si estás al día

Si no hay capítulos sin leer, el default es mostrar los leídos. Al
desmarcar la casilla, el form no manda `include_read` y el server vuelve al
default, así que la casilla se marca de nuevo. Haría falta mandar
`include_read=0` explícito.

---

## P2 — Consistencia entre pantallas

### 7. "leer →" significa dos cosas

- En `/releases`: POST que abre el lector **y marca leído**.
- En el home (widget de últimos releases): link directo al reader, **no
  marca**.

Mismo label, mismo diseño, comportamiento distinto.

### 8. Filtros: auto-submit en un lado, botón "aplicar" en otro

`/library` envía el filtro al cambiar el select (`data-auto-submit`);
`/library/entry` pide "aplicar". Además "marcar leído" en releases dice (en
el `title`) que marca también los anteriores; en el detalle no lo dice,
aunque hace lo mismo.

### 9. Navegación

- **[en vivo] Nav duplicado en desktop.** `.app-nav { display: none }`
  dentro del `@media (min-width: 64rem)` ([layout.html:251](../web/templates/layout.html))
  queda pisado por `.app-nav { display: flex }` más abajo
  ([layout.html:385](../web/templates/layout.html)), con la misma
  especificidad y declarado después. En desktop salen el sidebar **y** el
  header con "biblioteca · releases · settings". Es un bug de cascada, no de
  diseño.
- **[en vivo] Mobile apretado.** El logo `目次 mokuji` se parte en dos
  líneas y "SETTINGS" queda pegado al borde derecho (el nav llega a 373px de
  375).
- El estado `.active` del nav tiene CSS pero ningún template lo aplica: no
  sabes en qué sección estás.
- El home no está en el nav (solo vía logo).
- En **mobile** el header no tiene "tu perfil" ni "cerrar sesión"; el único
  logout en mobile es el botón suelto al final del home. En desktop hay
  dos (sidebar + home).

### 10. Sources con jerga operativa, arriba de todo

El detalle abre con una sección "sources" con `✓ matched`,
`⚠ matching incierto`, "reintentar", "desvincular". Es información de
operador, y contradice la línea de "mokuji decide el matching, no el user".
El empty state sugiere "vincular una source manualmente", pero esa UI no
existe.

### 11. Home flaco

Saludo, link a la biblioteca, últimos releases y un logout. No muestra "lo
que estás leyendo" ni "continúa donde ibas", que sería lo más útil al
entrar. **[en vivo]** Sin username, el saludo usa el email completo en
tipografía display y se sale del viewport en mobile
(`hola, ux-audit@example.test`).

### 11b. Visibilidad pública por default, sin preguntar

**[en vivo]** La cuenta nueva arranca con la biblioteca **pública** y el
onboarding no lo menciona (pregunta por el digest, no por la privacidad).
No es grave para un tracker de manga, pero sí es una decisión que el user
no tomó.

---

## P3 — Copy y detalles

- `Index · 12 entries` en el eyebrow de `/library` está en inglés.
- **[en vivo]** En mobile el placeholder del buscador se corta en "buscar
  tu biblioteca o", justo antes de lo importante ("agregar manga nuevo").
- **[en vivo]** En dark mode, los pasos inactivos del progreso del
  onboarding (`rgba(26, 24, 21, 0.13)`, hardcodeado) son invisibles: solo
  se ve el paso activo.
- `/favicon.ico` da 404 (y renderiza la página de error completa, 29 KB).
- Resultados del catálogo: el label dice `agregar nuevo · anilist` (el
  nombre interno de la source) y las descripciones vienen en inglés.
- "un solo email · **prolijo**" (onboarding y settings): en RD "prolijo" se
  lee como "largo, enredado", que es lo contrario de lo que se quiere decir.
- `og:locale` es `es_AR` en `entry.html` y `profile.html`, y `es_DO` en el
  resto.
- "acá" (detalle, releases) y "mail" (auth) desentonan con el resto del copy.
- El paso 2 del onboarding nombra MangaDex y Asura; el README incluye Comick.
- El perfil público lista los mangas sin link y con el `— cap. X` viejo.
- Dos "← biblioteca" en el detalle (arriba y abajo).
- Settings: username tiene su propio "guardar", y visibilidad + digest
  comparten otro. Si cambias username y visibilidad y le das a uno, el otro
  se pierde en silencio.

---

## Dirección propuesta (para discutir, en orden)

1. **Un solo modelo de progreso**: eliminar `last_chapter` y usar el cursor
   en todos lados (lista, perfil, detalle). Migración: copiar a cursor los
   `last_chapter` numéricos donde el cursor sea NULL.
2. **El detalle es el lugar donde se gestiona un manga**: estado, favorito,
   notas, URL y borrar pasan a `/library/entry`. La fila de la lista
   deja de ser `<details>` y es un link al detalle, con a lo más un
   cambio rápido de estado.
3. **El estado reacciona al progreso** (alcance a definir): elegir el estado
   al agregar; "por leer" → "leyendo" al marcar el primer capítulo; sugerir
   "completado" al llegar al final.
4. Arreglos de consistencia del P2 (leer →, nav activo, logout en mobile,
   filtros). Los bugs puros (nav duplicado, `include_read`, progreso del
   onboarding en dark) se pueden sacar ya, sin esperar las decisiones.
5. Estados honestos de las sources: distinguir "buscando" de "esta source
   no tiene capítulos", y un solo mensaje coherente entre la lista, las
   sources y el empty state.
6. Copy del P3.

Las decisiones 1–3 están entrelazadas. Conviene cerrarlas una por una antes
de tocar código.

---

## Feedback de usuarios (2026-10-19)

### F1. "Mangas duplicados con distintas portadas" (Haikyu!! vs Haikyuu!!)

No son duplicados: AniList devuelve el manga (2012, 404 caps) y los
one-shots piloto (2011, 1 cap). Mostramos `title.english` con fallback a
romaji, y los one-shots no tienen título en inglés, así que salen como
"Haikyuu!!". La query solo pide título, portada y descripción; no hay
formato, año, capítulos ni estado para distinguir. AniList los expone
(`format`, `startDate`, `chapters`, `status`).

### F2. "Todos los capítulos tienen la fecha de hoy"

Las fechas son de **subida a la source**, no de publicación. Comick deja
`publish_at` en null y usamos `created_at`
([comick.go:422](../internal/comick/comick.go)); cuando un scanlator sube
en lote, cientos de caps comparten el día. En la DB local: 692 caps de un
manga con fecha 2025-11-02, 477 de otro con 2025-09-14; MangaDex y Asura
también tienen lotes (170 y 105 caps en un día). El orden del detalle ya
es por número, pero la fecha visible confunde, y el select dice "más
recientes primero" cuando en realidad ordena por número.

### F3. MCP server para recibir avisos en ChatGPT

MCP es pull: el cliente llama tools, el server no empuja notificaciones.
Implica API pública nueva con auth (OAuth para MCP remoto). Ver propuesta.

### F4. Importar export de MyAnimeList

El export de MAL es XML con el ID de MAL, capítulos leídos y estado por
entrada. AniList mapea exacto por `idMal` (y acepta `idMal_in` en lotes de
50), así que no hay matching difuso. Capítulos leídos → cursor (encaja
con D1). El matching contra sources tiene que ser async: hoy agregar
tarda ~4 s por manga; el `Backfiller` ya rellena `manga_sources`
faltantes, pero solo corre al arrancar.

---

## Decisiones cerradas

### D1 — El cursor es la única fuente de progreso (2026-10-19)

`last_read_chapter_number` es la única verdad sobre "por dónde voy";
`last_chapter` desaparece.

- **Por qué:** el cursor es lo único conectado al sistema (badges,
  `/releases`, digest, "ver leídos"), es comparable con los capítulos y
  sirve igual para mangas sin source. En prod `last_chapter` estaba en
  desuso: 1 de 111 entries (`"252"`, cursor NULL), 0 no numéricas. Ese
  único caso mostraba el problema: progreso escrito a mano que los badges
  no veían.
- **Migración:** `cursor = MAX(cursor, last_chapter)` donde `last_chapter`
  sea numérico; lo no numérico se antepone a `notes` como
  `progreso anterior: …` (no hay casos en prod, pero sí puede haberlos en
  instancias self-hosted); después drop de la columna.
- **Superficie:** fuera el input de texto y el campo del export; lista,
  detalle y perfil público muestran el cursor (nada si es NULL o 0).

### D2 — El detalle es donde se gestiona un manga (2026-10-19)

`/library/entry/{id}` concentra todas las acciones; `/library` es solo
navegación.

- **Detalle, de arriba a abajo:** header con estado como tres botones
  (`leyendo · por leer · completado`, un click, sin "guardar") y favorito
  como toggle; cursor; capítulos; bloque "notas y fuente" con su propio
  guardar; borrar al fondo con confirmación (redirige a `/library`).
- **Lista:** cada fila es un link al detalle (portada, estado, título,
  `— cap. N` del cursor, badge). Sin `<details>` ni form.
- **Por qué:** todo ya enlaza al detalle (título, badges, releases, email);
  cambiar estado pasa a dos clicks visibles en vez de tres con uno
  escondido; cada acción vuelve al detalle, lo que elimina la pérdida de
  filtro (#5); todo sigue siendo POST sin JS (ADR 0002).
- **Descartado:** cambio de estado desde la lista. Vuelve a llenar las filas
  de controles y en mobile no caben tres botones por fila. Se reabre si
  aparece como queja real.
- **Técnico:** `POST /library/{id}/update` sobrescribe todos los campos, así
  que se reemplaza por endpoints chicos (`/status`, `/favorite`, `/notes`)
  para que un botón no borre lo demás.

### D3 — El estado reacciona al progreso, pero solo hacia "leyendo" (2026-10-19)

Hallazgo previo: el estado no es solo una etiqueta. El worker, `/releases`
y el digest solo consideran entries en `reading`; "por leer" y
"completado" no se pollean ni notifican, y la UI no lo dice.

- **Regla visible:** bajo los botones de estado del detalle, "solo te
  avisamos de capítulos nuevos de lo que estás leyendo."
- **Al agregar:** sigue entrando como `leyendo` (el valor principal es el
  aviso; arrancar sin tracking deja huérfanos a los mangas a mitad), pero
  redirige al detalle del manga nuevo, donde el siguiente paso natural es
  poner el cursor. Costo aceptado: agregar varios desde una búsqueda
  requiere volver atrás.
- **`por leer` → `leyendo` automático** con cualquier progreso: marcar
  leído, "leer →", link del email, cursor > 0. Solo desde "por leer";
  nunca toca "completado" (relecturas).
- **Nada automático ni sugerido hacia `completado`:** llegar al último
  capítulo casi siempre es "al día", no "terminado", y no guardamos el
  estado de publicación. Se reabre si algún día lo guardamos.

### D4 — Sin home: la biblioteca es la entrada (2026-10-19)

El home era un `/releases` recortado (los primeros 5 releases sin leer)
con un "leer →" que no marcaba leído.

- **`/` con sesión redirige a `/library`.** Se elimina `home.html` y el
  widget. El logo apunta a `/library`; el nav queda biblioteca · releases ·
  settings.
- **Orden de la lista:** estado → tiene capítulos nuevos → favorito →
  `updated_at`. Lo pendiente de leer queda arriba al entrar.
- **Logout en mobile** pasa al final de `/settings` (el sidebar de desktop
  no cambia). El nav mobile no crece.
- **Nudge de username** se elimina; `/settings` ya lo explica. Si se quiere
  empujar, el lugar es el onboarding (otra discusión).
- **Por qué no rediseñar el home:** cualquier home útil termina siendo una
  lista de mangas con badges de nuevos, que es la biblioteca ordenada así.
- **Arreglos incluidos:** `.active` en el nav; nav duplicado en desktop
  (cascada CSS); auto-submit de filtros en el detalle; `include_read=0`
  explícito al desmarcar; pasos del onboarding visibles en dark mode.

### D5 — Estados de sources honestos, un solo vocabulario (2026-10-19)

El dato ya existe: el matcher deja `next_check_at` NULL y el worker lo
setea al terminar un poll; `consecutive_failures` marca fallas.

- **Por source:** `N capítulos` (polleada, con caps) · `buscando
  capítulos…` (`next_check_at` NULL) · `sin capítulos aquí` (polleada, 0
  caps) · `no responde, reintentamos solos` (`consecutive_failures > 0`) ·
  `no lo encontramos` (`needs_review` y `no_results` juntos: para el user
  es lo mismo y no tiene cómo resolver la duda).
- **En el detalle:** una línea de resumen bajo el título ("capítulos de
  mangadex · asura y comick no lo tienen"); la lista por source con
  reintentar/desvincular pasa a un `<details>` cerrado al final.
- **Empty state y badge salen del mismo cálculo.** Badge con cuatro
  valores: `N nuevos · al día · buscando… · sin capítulos`. Fuera
  `pendiente`, `matching incierto`, `sin source` y la mención a "vincular
  manualmente", que no existe.

### D6 — Sin fechas en el detalle; "sigue con el cap. N" arriba (2026-10-19)

Responde a F2.

- **Fuera la fecha de cada fila de capítulo en el detalle.** Es fecha de
  subida a la source, no de publicación; con lotes miente. El número
  identifica el capítulo y la lista ya va ordenada por número.
- **Orden renombrado:** "del último al primero" / "del primero al último".
- **CTA arriba del detalle:** "sigue con el cap. N →" (primer capítulo
  después del cursor; abre el lector y marca leído, como "leer →"). Al día:
  "al día · último: cap. N".
- **`/releases` conserva la fecha:** para capítulos realmente nuevos, subida
  ≈ publicación.
- **Descartado:** esconder la fecha solo cuando "parece un lote". Heurística
  que igual falla, para salvar un dato que no es confiable.

### D7 — La búsqueda distingue formato, año y estado (2026-10-19)

Responde a F1.

- **Línea de metadata** por resultado desde AniList (`format`,
  `countryOfOrigin`, `startDate.year`, `chapters`, `status`):
  `manga · 2012 · 404 caps · terminado`. Tipo = formato + país (`MANGA`+KR
  → manhwa, +CN → manhua; `one-shot`, `novela`). Capítulos solo si vienen
  (null en emisión). Estado en español.
- **Romaji como título secundario** cuando el principal está en inglés.
- **One-shots y novelas al final**, no ocultos. Descartado ordenar por
  popularidad: rompe búsquedas exactas de nicho.
- **Descripción a 2 líneas**; "agregar nuevo · anilist" → "agregar nuevo".
- **No se persiste** la metadata en `manga`; solo query + `SearchResult`.
  Se decide guardarla si se usa en otro lado (p. ej. sugerir "completado",
  abierto en D3).

### D8 — Copy en español dominicano y settings por sección (2026-10-19)

- **Nav:** `biblioteca · nuevos · ajustes` (h1 de settings → "ajustes").
  Todo en español, coincide con los títulos y ahorra ancho en mobile. Las
  URLs no cambian.
- **Reemplazos:** "prolijo" → "limpio" (landing, meta, `llms.txt`) y
  "resumido" (digest semanal); `Index · N entries` → `índice · N
  entradas`; "acá" → "aquí"; "mail" → "email"; `og:locale es_AR` →
  `es_DO`.
- **Onboarding paso 2:** lista las sources habilitadas desde el config en
  vez de nombrarlas a mano.
- **Settings:** un form y un botón por sección (username, visibilidad,
  digest); al final cerrar sesión (D4), exportar y borrar cuenta.
