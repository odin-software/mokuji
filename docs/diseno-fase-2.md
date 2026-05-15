# Diseño: Fase 2 — Release tracking

> **Status**: borrador colaborativo. Doc vivo — se va llenando a medida que cerramos decisiones. Al final destilamos un ADR (probablemente 0013) + plan de implementación dividido en commits.
>
> **Cómo usar este doc**: cada dimensión tiene contexto, preguntas concretas, trade-offs y una recomendación tentativa mía con razones. Escribí tu respuesta en el bloque `> **Tu respuesta:**`. No hace falta responder en orden — algunas dimensiones desbloquean otras (lo marco). Cuando una decisión queda cerrada, la movemos a "Decidido" al final.

---

## Objetivo (qué problema resuelve fase 2)

Fase 1 dejó la app utilizable como tracker manual: marcás capítulo leído cuando lo leíste. Fase 2 quiere cerrar el loop: que la app te avise cuando hay capítulo nuevo de algo que estás siguiendo, sin que tengas que ir a MangaDex/scanlator a chequear.

> **Tu respuesta** (refinar / corregir el objetivo en una o dos frases, o agregar matices):

---

## No-objetivos (qué NO queremos en fase 2)

Cosas que es útil declarar explícitamente como fuera de scope para evitar scope creep durante implementación. Ejemplos posibles que se me ocurren — confirmar/agregar/eliminar:

- Multi-source (Mangaplus, Comikey, oficiales). Empezamos con MangaDex solamente.
- Push notifications nativas (web push). El producto se notifica por email y/o badge in-app.
- Lectura embebida (mokuji no es un reader, no servimos imágenes de capítulos).
- Ratings/social. Sigue fuera, igual que en fase 1.

> **Tu respuesta** (qué más declarar como no-objetivo, qué quitar de esta lista):

---

## Dimensión 1 — Scope del MVP

**Contexto.** Hay un eje "in-app vs email vs ambos" y un eje "cobertura" (solo `status=reading` o también `planned`?). El MVP más chico que vale la pena lanzar es el que cubre el caso 80%: usuario con varios mangas en `reading` quiere ver cuando hay capítulo nuevo. Email digest agrega valor pero también complejidad (templates, opt-in, unsubscribe, cadencia).

**Preguntas:**

1. ¿La superficie principal es in-app (página `/releases`) o email, o ambas en paralelo?
2. ¿Cubrimos solo mangas con `status=reading` o también `planned` y `completed`?
3. ¿Qué pasa con mangas que el usuario tiene como `dropped` o sin status? (no tenemos esos statuses hoy — confirmar si fase 2 los necesita)
4. Si email entra al MVP: ¿cadencia fija (semanal por ejemplo) o configurable desde día 1?
5. Badges in-app (ej. número en el nav indicando "5 caps nuevos sin ver") — ¿MVP o post-MVP?

**Trade-offs:**

- In-app solo es lo más barato y suficiente para validar la mecánica antes de meter email.
- Email digest se siente cerca del producto que vos describiste ("la segunda razón principal") pero suma: SMTP, templates, opt-in, unsubscribe link, possible test mode para no spamear durante dev.
- Cadencia configurable desde día 1 es 1-2 días extra de trabajo + setting nuevo en /settings. Cadencia fija + setting opt-in (on/off) cubre el 90% sin esa fricción.

**Recomendación tentativa.** MVP = página `/releases` + email semanal opt-in (default off) + badge in-app simple. Cobertura: solo `status=reading`. Cadencia fija (semanal, domingo a la mañana). Razón: cubre las dos razones del producto sin meter ejes configurables que se pueden agregar después si alguien los pide.

> **Tu respuesta:**

1. Puede ser una pagina como /releases, o puede que eso sea parte del home page y tambien tenga la pagina.
2. Solo status reading definitivamente.
3. Nada pasa con dropped, si no esta estatus reading, no nos importa la actualizacion.
4. Si entra el email con cadencia fija sí. Lo que debemos de tener algún tipo de mecanismo para probar que sí funcione.
5. Post-MVP en verdad.

Muy importante para mi es que desuscribirse y todo lo que tenga que ver con opt out sea claro y funcione a la perfeccion.
Incluso, el exportar toda la data y borrar toda la data de un usuario es algo que tenemos que hacer sí o sí, y debe ser parte del MVP.

**Respuesta mía (sintetizando):**

- Página `/releases` + widget "últimos releases" en el home cuando hay sesión. La página es la fuente de verdad; el widget es un teaser que linkea ahí. Cuesta poco.
- Cobertura: solo `reading`. Confirmado.
- Email semanal cadencia fija. Mecanismo de prueba propuesto: comando interno tipo `make send-test-digest USER=mi@mail.com` o un botón "enviar digest de prueba" oculto en /settings (solo para admin o todos). Decidir cuál tiene más sentido.
- Badge in-app post-MVP. Confirmado.
- **Export + delete data se promueve a Dimensión 11 nueva** abajo. Es importante y merece tratamiento propio (afecta endpoints, UX, decisión sobre formato JSON vs CSV, soft-delete vs hard-delete, etc.).

Aqui te agrego que puede que simplemente el template funcione bien en mailpit y eso ayude, o un endpoint que funcione con un token en especifico, algo asi.

---

## Dimensión 2 — Visibility / privacy

**Contexto.** Hoy un usuario puede tener su lista pública o privada (ADR 0005 / users.list_visibility). Los releases derivan de la library, así que hay que decidir cómo se exponen.

**Preguntas:**

1. La página `/releases` ¿es siempre privada (solo el dueño la ve) o tiene variante pública (`/u/{username}/releases`)?
2. Si un usuario tiene la lista privada, ¿el badge in-app sigue funcionando para él (obvio que sí)?
3. ¿Algún dato derivado (ej. "30 usuarios siguen este manga") se expone alguna vez? Probablemente no — confirmar.

**Recomendación tentativa.** `/releases` es siempre privada (solo el dueño autenticado). Sin variante pública en fase 2. Sin métricas agregadas expuestas. Razón: minimiza superficie de privacy y simplifica controlador.

> **Tu respuesta:**

1. 100% privada.
2. Obvio que sí.
3. No, por ahora no tenemos ningún feature social, no significa que nunca, pero no por ahora.

---

## Dimensión 3 — Matching AniList → MangaDex

**Contexto.** Hoy los mangas entran a la library vía AniList search. Un manga tiene `source='anilist'` y `source_id=<anilist-id>` en la tabla. Para ver releases hay que conectar ese manga a su entrada en MangaDex (que tiene su propio UUID). MangaDex tiene endpoint de búsqueda por título pero los títulos varían (japonés, romaji, inglés, idiomas alternativos, especiales). Esto da falsos positivos / negativos.

**Preguntas:**

1. ¿El matching es automático (worker intenta buscar por título cuando se agrega o periódicamente) o manual (al agregar a la library, mostramos resultados de MangaDex y el usuario confirma)?
2. Si automático: ¿qué hacemos con baja confianza? Posibles caminos: marcar como "needs review", elegir el primer match, omitir el manga hasta intervención manual.
3. ¿Permitimos al usuario corregir el match desde la UI de un entry? (`/library/entry/{id}` → "este no es el manga correcto, buscar otro")
4. ¿Hay alguna API de cross-reference que ya conozcas? (e.g. AniList ↔ MangaDex via algún dump comunitario tipo manga-tracker, anitomy, etc.)

**Trade-offs:**

- Automático full = menos fricción para el usuario, pero riesgo alto de matchear el manga equivocado (especialmente con títulos comunes).
- Manual al agregar = más fricción (un paso extra al agregar) pero alta confianza.
- Híbrido: intentamos automático con score de confianza; si baja, mostramos un prompt para confirmar.

**Recomendación tentativa.** Híbrido. Al agregar el manga (o lazy, al pedir releases por primera vez de ese manga), buscar en MangaDex por título(s) que tenemos de AniList (puede ser romaji + inglés). Si hay match con score alto y único, autolinkear. Si hay ambigüedad (varios títulos parecidos) o nada, marcar el entry con un flag `needs_mangadex_match=true` y mostrarle al usuario en `/library/entry/{id}` un selector para confirmar manualmente. Razón: barato en el caso bueno, sin falsos positivos silenciosos en el caso malo.

> **Tu respuesta:**

1. No entiendo esta.
2. No entiendo tampoco.
3. Necesito que expliquemos esto bien la verdad, esta dimension entera.
4. Ni idea.

Aunque leyendo tu recomendación, eso tiene mucho sentido en verdad.

**Explicación más clara (perdón, fue muy denso de un golpe):**

**El problema concreto.** Hoy un usuario agrega un manga así:

1. En `/library/search` busca "chainsaw man".
2. Le mostramos resultados de **AniList** (vienen vía GraphQL de la API de AniList).
3. Hace click en "agregar". Guardamos en la DB: `source='anilist'`, `source_id=105778`, `title='Chainsaw Man'`, `cover_url=<URL de AniList>`.

Eso es lo que tenemos hoy. **AniList tiene metadata del manga pero NO publica capítulos individuales** (ni los caps, ni cuándo se publicaron, ni links a leerlos). Para los caps necesitamos otra fuente: **MangaDex**.

**MangaDex** es otro sitio, completamente separado, con sus propios IDs (UUIDs tipo `a1c7c817-4e59-43b7-9365-09675a149a6f`). Tiene su propia API que te deja preguntar "dame todos los capítulos del manga con UUID X". El problema: **AniList y MangaDex no comparten IDs**. AniList no sabe del UUID de MangaDex y viceversa. Son catálogos independientes que casualmente describen los mismos mangas.

**Entonces, la pregunta del "matching"** es: ¿cómo conectamos `manga con AniList ID = 105778` con `manga con MangaDex UUID = a1c7c817-…`? Sin esa conexión, no podemos preguntarle a MangaDex por los caps de un manga que el user agregó vía AniList.

**Opciones para hacer el match:**

A. **Automático (worker hace todo solo)**. Cuando se agrega un manga, en background, el worker toma el título ("Chainsaw Man") y se lo pasa a la API de MangaDex tipo "buscar manga con este título". MangaDex devuelve una lista de resultados. El worker elige el primero o el de mejor score y guarda el UUID. **Riesgo**: si el título es ambiguo ("Berserk" puede tener spin-offs, "One Piece" puede tener doujinshi), elige el equivocado y todos los caps que mostramos son del manga incorrecto. El usuario no se entera porque no hay UI de confirmación.

B. **Manual (siempre confirma el user)**. Al agregar el manga, en lugar de un solo botón "agregar", mostramos también resultados de MangaDex al lado de los de AniList. El user tiene que elegir cuál es el correcto en MangaDex. **Cuesta**: un paso extra al agregar cualquier manga, aunque el match sea obvio.

C. **Híbrido (mi recomendación)**. Automático cuando el match es claro, manual cuando es ambiguo. Concretamente:

- Al agregar, hacemos la búsqueda en MangaDex.
- Si hay UN solo resultado con score alto (título idéntico o muy parecido), guardamos el UUID y listo, transparente al user.
- Si hay varios resultados parecidos, o ninguno con score alto, marcamos el manga como `mangadex_status='needs_review'` y en `/library/entry/{id}` mostramos un selector: "no pudimos linkear este manga automáticamente, ¿cuál de estos es?".
- El user siempre puede ir a `/library/entry/{id}` y decir "este match está mal, buscar otro" aunque haya sido automático.

**Sobre la pregunta 4** (API de cross-reference): existen dumps comunitarios tipo [manga-tracker-bot](https://github.com/manga-tracker) que mantienen mapeos AniList↔MangaDex↔otros. Son útiles pero (a) dependencia externa con confianza variable, (b) muchos mangas no están mapeados. Vale más confiar en búsqueda directa por título y tener fallback manual. Lo descartamos como dependencia, pero podemos consultarlo como hint si en el futuro vemos que la búsqueda por título falla mucho.

¿Tiene sentido ahora? Si sí, confirmás la recomendación C y avanzamos.

Ahora tiene sentido sí, vamos a por C.

---

## Dimensión 4 — Storage / data model

**Contexto.** Tenemos que persistir capítulos. Hay dos preguntas: (a) qué tablas y (b) qué normalización (¿el cap se guarda una vez por manga y todos los users lo comparten? ¿o por user?).

**Esquema candidato (a refinar):**

```sql
-- 1. Vínculo AniList ↔ MangaDex (normalizado, compartido entre users)
ALTER TABLE mangas ADD COLUMN mangadex_id TEXT;        -- UUID de MangaDex, NULL si no matcheado
ALTER TABLE mangas ADD COLUMN mangadex_status TEXT;    -- 'unmatched'|'matched'|'needs_review'|'no_results'
ALTER TABLE mangas ADD COLUMN mangadex_checked_at TIMESTAMP;

-- 2. Capítulos del manga (compartido entre users)
CREATE TABLE chapters (
  id INTEGER PRIMARY KEY,
  manga_id INTEGER NOT NULL REFERENCES mangas(id),
  source TEXT NOT NULL,        -- 'mangadex' por ahora
  source_id TEXT NOT NULL,     -- UUID del cap en MangaDex
  number TEXT,                 -- '125', '125.5' (puede ser no-numérico tipo "Special")
  title TEXT,
  language TEXT,               -- 'en', 'es', 'pt-br', etc.
  published_at TIMESTAMP,      -- fecha de publicación según MangaDex
  external_url TEXT,           -- link al reader de MangaDex
  created_at TIMESTAMP NOT NULL DEFAULT (datetime('now')),
  UNIQUE(source, source_id)
);

-- 3. Estado de lectura del cap por usuario (chapter linking)
-- Opcional para fase 2 según scope; bloquea/desbloquea backlog item #2
CREATE TABLE chapter_reads (
  user_id INTEGER NOT NULL REFERENCES users(id),
  chapter_id INTEGER NOT NULL REFERENCES chapters(id),
  read_at TIMESTAMP NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (user_id, chapter_id)
);
```

**Preguntas:**

1. ¿La tabla `chapter_reads` entra en fase 2 (= chapter linking incluido) o la dejamos para fase 3?
2. Idioma del cap: ¿guardamos solo inglés / solo español / todos los idiomas que MangaDex devuelve? Si solo uno, ¿quién elige?
3. ¿Borramos chapters muy viejos para mantener la DB chica, o crecemos sin podar? (MangaDex tiene mangas con 500+ caps).
4. El campo `number` lo dejo TEXT porque hay caps tipo "8.5", "Special", "Extra" — confirmás?

**Recomendación tentativa.** Esquema como arriba, con: (a) `chapter_reads` SÍ entra a fase 2 para cerrar el loop completo (sino el badge "5 caps nuevos" no funciona — necesita saber qué leíste); (b) idioma — empezamos guardando solo inglés (más cobertura en MangaDex), agregamos un setting de idioma preferido por usuario en fase 2.5 si alguien lo pide; (c) sin podar hasta que duela.

> **Tu respuesta:**
> La recomendación está perfecta, encaja a lo que quisiera.

---

## Dimensión 5 — Worker design

**Contexto.** Necesitamos un job en background que polleea MangaDex periódicamente y persiste capítulos nuevos. Preguntas de diseño: cadencia, ¿por manga o batch?, ¿cómo manejamos rate limits de MangaDex?, ¿qué pasa si el server reinicia mid-poll?

**Lo que sé / suposiciones a verificar contra docs de MangaDex:**

- MangaDex tiene endpoint `/manga/{id}/feed` que devuelve caps de ese manga.
- Rate limit aproximado: 5 req/seg global (verificar).
- No requiere auth para reads.

**Preguntas:**

1. Cadencia del worker: ¿cada N horas (ej. cada 6h)? ¿Diferente para `status=reading` (más frecuente) vs `planned` (menos)?
2. Estrategia: ¿iteramos por todos los mangas matcheados en cada tick, o tenemos una cola con prioridades / next_check_at por manga?
3. Si el binario se reinicia (deploy), ¿perdemos el poll en curso? ¿Hacemos checkpoint?
4. Concurrencia: ¿un solo manga a la vez (simple, lento) o N goroutines con limit (más complejo, más rápido)?
5. Backoff: si MangaDex devuelve 429 o 5xx, ¿cómo retrocedemos?

**Recomendación tentativa.** Goroutine única en el proceso de mokuji (no un binario separado), schedule con `time.Ticker` cada 1h. En cada tick, query SQL: `SELECT mangas where mangadex_id NOT NULL AND (next_check_at IS NULL OR next_check_at < now) ORDER BY next_check_at LIMIT 50`. Por cada manga, hacer 1 req a `/manga/{id}/feed` con backoff exponencial, persist nuevos caps, setear `next_check_at = now + 6h`. Esto evita batch huge cuando hay muchos users y manejes rate limit naturalmente. Si binario reinicia, no perdemos nada — siguiente tick toma desde donde quedó.

> **Tu respuesta:**
> Suena bien la recomendación. Definitivamente no un binario separado.

---

## Dimensión 6 — Notificación (email + badges in-app)

**Contexto.** Asumiendo que email entra al MVP (depende de Dimensión 1).

**Preguntas:**

1. Cadencia del digest: ¿semanal fijo (domingo 9am UTC)? ¿adaptativo (solo si hay caps nuevos)?
2. ¿Email se enriquece con cover, título, fecha + link directo a MangaDex, o es texto plano con "X caps nuevos, ver en mokuji.org/releases"?
3. Opt-in vs opt-out al crear cuenta. Default off es respetuoso pero genera adopción 0%; default on es más invasivo.
4. Unsubscribe: ¿link en cada email (best practice / CAN-SPAM) o solo toggle en /settings?
5. Badge in-app: ¿contador en el nav, o algo más sutil tipo marcador en cada entry?

**Recomendación tentativa.** Cadencia semanal, adaptativo (skip si no hay caps nuevos esa semana). Email plain-text por ahora (siguiendo ADR 0007 — HTML emails cuando haya marca). Opt-in al setting (default off) con toggle claro en /settings + link de unsubscribe en cada email que respeta el toggle. Badge in-app simple: número junto a "biblioteca" en el nav cuando hay caps nuevos no vistos.

> **Tu respuesta:**

1. Como expliqué arriba, semanal fijo, con forma de probarlo. Creo que no debería mandarse si no hay capítulos nuevos.
2. "texto plano", con titulo, fecha y link. Debería verse bien, pero ya eso sería cuestión de diseño del email.
3. Default on, tal vez con advertencia y aclarar que siempre se puede hacer opt out en las preferencias.
4. 100% unsubscribe, investigar como hacer que funcione con la opción de "darse de baja" que pone Gmail.
5. No sé, por ahora está bien lo de numero junto a "biblioteca".

**Respuestas/pushback míos:**

**Punto 1.** ✅ Cerrado. Semanal, adaptativo (skip si no hay caps). Mecanismo de prueba se discute en Dim 1 arriba.

**Punto 2 — "texto plano que se vea bien" tiene una contradicción que vale aclarar.** Plain-text (lo que dice ADR 0007 hoy) no tiene formato: no hay bullets, no hay tipografía, no hay enlaces visualmente distinguidos del texto. Si querés que "se vea bien", hay tres caminos:

- **(i) Plain estructurado**: usando ASCII a la antigua (líneas en mayúsculas como títulos, separadores `---`, URLs visibles tipo `https://mokuji.org/m/123`). Funciona en cualquier cliente, pero "limpio funcional", no "lindo".
- **(ii) Multipart plain + HTML básico** (best practice de email transaccional). Mandamos los dos cuerpos en el mismo email; cliente decide cuál renderizar. HTML básico = `<h2>`, `<ul>`, `<a>`, sin CSS ni imágenes ni layout complicado. Compatible con todo. Acepta marca después cuando esté.
- **(iii) HTML pleno con CSS inline + cover images**. Lo bonito, pero contradice ADR 0007 ("HTML emails cuando exista marca") y agrega trabajo de templates testeados contra clientes raros (Outlook, Gmail dark mode).

**Mi recomendación**: (ii). Si lo aceptás, hay que actualizar ADR 0007 (¿reemplazarlo con un ADR nuevo o anotarlo como evolución?). Hoy ADR 0007 solo cubre auth emails (que SÍ pueden seguir siendo plain).

Respuesta: Ok, Sería la 2 por ahora entonces, luego con marca vemos.

**Punto 3 — Pushback sobre "default ON".** Pelo en la sopa serio acá. Default ON sin opt-in explícito al signup es problemático:

- **GDPR (UE) y LGPD (Brasil)** requieren consentimiento "freely given, specific, informed and unambiguous". Default ON con email pre-suscrito ≠ unambiguous. Multa potencial real si alguien reporta.
- **Gmail/Apple Mail** crecientemente penalizan dominios con tasas altas de unsubscribe/spam. Default ON = más unsubscribes en los primeros emails = mokuji.org en spam folder para todos.
- **CAN-SPAM (USA)** tolera más pero igual requiere unsubscribe funcional y no engañoso.

**Alternativas que dan adopción alta sin riesgo:**

- **(A) Opt-in al signup con default ON visible**: en la página de signup hay un checkbox "Recibir email semanal con releases de mi biblioteca" pre-tildado. El user lo ve y puede destildar antes de crear cuenta. Esto cumple "unambiguous" porque el user explícitamente acepta al crear la cuenta. Es lo más cercano a tu instinto y legalmente seguro.
- **(B) Default OFF + prompt destacado en el dashboard** cuando el user tiene 1+ manga en `reading`: "¿Querés recibir email semanal con los caps nuevos de tu biblioteca?". Aparece justo cuando le importa. Adopción alta sin pre-suscripción.
- **(C) Tu propuesta (default ON sin checkbox visible al signup)**: rápido pero arriesgado.

Mi recomendación: **(A)**. Cuál preferís?

Respuesta: Vamos con A entonces.

**Punto 4 — List-Unsubscribe (Gmail "darse de baja").** Sí, es estándar: **RFC 2369 + RFC 8058 (One-Click)**. Dos headers en cada email saliente:

```
List-Unsubscribe: <https://mokuji.org/unsubscribe?token=...>, <mailto:unsub@mokuji.org?subject=...>
List-Unsubscribe-Post: List-Unsubscribe=One-Click
```

El token es un JWT-ish HMAC firmado con secret server-side, codifica `user_id + scope=releases_digest`. Endpoint `POST /unsubscribe` (sin auth, valida el token, marca al user como unsubscribed). Sin sesión necesaria. Trivial de implementar (~40 LOC). Gmail muestra "darse de baja" arriba del email cuando estos headers están presentes.

Confirmás que esto entra a Dim 11 (export/delete) o lo tratamos acá? Yo lo dejaría como subtema de Dim 6 (es parte del flujo de email).

Respuesta: De acuerdo. Acá.

**Punto 5.** ✅ Cerrado. Badge numérico al lado de "biblioteca". Post-MVP.

---

## Dimensión 7 — Histórico

**Contexto.** Cuando un usuario agrega un manga que ya tiene 100 caps publicados, ¿qué guardamos? Opciones:

- (a) Todos los caps históricos en la DB.
- (b) Solo el último cap conocido como "cursor"; capítulos nuevos son los publicados después.
- (c) Caps desde una fecha (ej. los últimos 30 días) en adelante.

**Trade-offs:**

- (a) infla la DB pero permite features como "ver progreso histórico", "marcar caps viejos como leídos".
- (b) DB chica pero perdés capacidad de mostrar histórico, y si el primer poll falla parcialmente puede haber gaps.
- (c) compromise — útiles "recientes" sin almacenar todo.

**Recomendación tentativa.** (a) — guardamos todos los caps. Razón: SQLite escala bien (un manga con 500 caps son 500 filas, nada), y nos da flexibilidad para chapter_reads históricos y vistas como "leíste 12/45". El gotcha es el primer poll por manga, que trae muchos caps de una; lo manejamos con paginación de la API MangaDex.

> **Tu respuesta:**
> Bueno, si es posible hacerlo bien con el API de MangaDex pues hagámoslo. para mi uno debería de agregar entonces a la librería con opción de ya lei todos los que van, algo así.

**Mi respuesta:** ✅ Buena idea, lo agrego al spec. Detalle de implementación cuando lleguemos al commit de UI:

- Al agregar un manga vía `/library/search`, después del match con MangaDex, mostrar un toggle/checkbox: "Marcar todos los capítulos publicados como leídos". Default tildado para mangas con `status=reading` (asumimos que si lo agregás como reading, ya lo venías leyendo). Default destildado si lo agregás como `planned`.
- Internamente: setear el cursor del user (ver Dim 9) al cap más reciente al momento del add.
- Si después aparece un cap nuevo, ya cuenta como "no leído" porque es posterior al cursor.

Confirmá si lo querés así o con otro default.

Respuesta: Confirmado.

---

## Dimensión 8 — Idioma / scanlation

**Contexto.** MangaDex es agregador — el mismo cap puede tener varias versiones (grupos de scanlation distintos, idiomas distintos). El feed devuelve todas las versiones.

**Preguntas:**

1. ¿Mostramos un cap por número (la "primera" versión que apareció) o todas las versiones?
2. ¿El usuario configura idioma preferido en /settings o asumimos inglés?
3. ¿Filtramos por scanlation group? (probablemente no — overkill).

**Recomendación tentativa.** Por cap, mostramos solo la versión en idioma preferido del usuario (default `en`). Setting de idioma preferido entra en fase 2 pero con valores reducidos: `en`, `es`. Otros idiomas se agregan cuando alguien los pida. Sin scanlation filter.

> **Tu respuesta:**
> La recomendación está nítida.

---

## Dimensión 9 — Marcar como leído (chapter linking, backlog #2)

**Contexto.** Backlog item #2 era "chapter linking — depende de fase 2". La pregunta es si entra en fase 2 o va a fase 3. Si entra: necesitamos UI para marcar un cap como leído, sincronizar con el `last_chapter` del entry actual, y manejar caps "saltados" (leí el 5 pero no el 4).

**Preguntas:**

1. ¿Marcar como leído entra a fase 2 o queda para fase 3?
2. Si entra: ¿auto-actualiza el `last_chapter` del entry cuando marcás un cap como leído? ¿O son independientes?
3. ¿Caps saltados se pueden marcar individualmente (= chapter_reads granular) o forzamos "leídos hasta el cap X" (= solo last_chapter)?

**Recomendación tentativa.** Entra a fase 2 pero versión simple: tabla `chapter_reads` granular (un row por cap leído por user) + auto-actualizar `last_chapter` del entry como `MAX(number) de chapter_reads de ese user/manga`. Eso cubre los dos modos sin agregar UI compleja.

> **Tu respuesta:**
> Que tal si solo guardamos como si fuera un cursor de por donde va el usuario, porque no creo que la gente lea capítulos salteados, al menos yo no lo hago. Qué crees de esto? Acepto pushback.

**Mi respuesta — acepto tu propuesta con un refinamiento técnico:**

Tenés razón. La mayoría lee lineal, granular es over-engineering para el caso real. El cursor cubre el 95% de los flujos. **Aceptado**.

Pero hay un gotcha técnico que vale resolver acá: ¿qué tipo es el cursor?

- **Opción 1: cursor por número** (`last_read_chapter_number TEXT`). Problema: ordenar "8.5" vs "10" como string da `"10" < "8.5"`. Hay que convertir a float para comparar, y eso falla con "Special". Truco posible: padding (`0008.5`, `0010.0`) o columna numérica auxiliar para sort.
- **Opción 2: cursor por chapter_id** (`last_read_chapter_id INTEGER REFERENCES chapters(id)`). El cap "no leído" se define como `published_at > (SELECT published_at FROM chapters WHERE id = last_read_chapter_id)`. Simple, rápido, no necesita parsear números.

**Recomendación**: Opción 2 (cursor por chapter_id). Más simple en SQL, no rompe con caps "Special" o "8.5".

Lo que esto implica para el schema (modifica la propuesta de Dim 4):

- Elimino tabla `chapter_reads` (ya no la necesitamos).
- Agrego columna `last_read_chapter_id INTEGER REFERENCES chapters(id)` en `library_entries`.
- "Caps no leídos del user X en manga M" = `SELECT * FROM chapters WHERE manga_id = M AND published_at > (SELECT published_at FROM chapters WHERE id = (SELECT last_read_chapter_id FROM library_entries WHERE user_id = X AND manga_id = M))`. SQL un poco anidado pero straightforward.

Esto simplifica todo el modelo de datos. ¿Confirmás?

Confirmo opcion 2.

---

## Dimensión 10 — Abstracción para sources futuros

**Contexto.** Ya tenemos `internal/mangasource` con interface `Source` que abstrae AniList. Análogo: `internal/releasesource` con interface que abstrae MangaDex (y eventualmente Mangaplus, etc.).

**Preguntas:**

1. ¿Vale ya la interface o empezamos hardcoded a MangaDex y abstraemos cuando aparezca un segundo provider? (YAGNI vs preparar terreno)
2. Si abstracción desde día 1: ¿qué métodos? `SearchByTitle(string) []Match`, `FetchChapters(sourceID, since timestamp) []Chapter`, etc.

**Recomendación tentativa.** Hardcoded a MangaDex en fase 2 (un solo paquete `internal/mangadex`), siguiendo el principio YAGNI. Cuando aparezca un segundo provider (cosa que probablemente no pase en 6 meses), refactorizamos a interface. El costo de la refactor es bajo porque el caller (worker) ya está aislado.

> **Tu respuesta:**
> De acuerdo con la recomendación.

---

## Dimensión 11 — Data export + account deletion (must-have del MVP)

**Contexto.** Vos lo identificaste como must-have en Dim 1: "el exportar toda la data y borrar toda la data de un usuario es algo que tenemos que hacer sí o sí, y debe ser parte del MVP". Es decisión correcta tanto por filosofía Odin (file-over-app, el usuario es dueño de sus datos) como legal (GDPR Article 15 "right of access" + Article 17 "right to erasure").

**Preguntas:**

1. **Formato del export**: ¿JSON (estructurado, machine-readable), CSV (Excel-friendly), o ambos?
2. **Scope del export**: ¿solo data del user (perfil, library, settings) o también data derivada (caps leídos, chapters trackeados)?
3. **Entrega**: ¿descarga directa desde `/settings/export` (genera al vuelo, devuelve archivo)? ¿Email con link de descarga firmado? ¿Cola de jobs si el export es grande?
4. **Account deletion — confirmación**: ¿typed password (no tenemos, somos OTP-only), re-OTP por email, frase confirmatoria tipo "borrar mokuji"?
5. **Account deletion — alcance**: ¿hard delete (DELETE CASCADE de todo) o soft delete con timeout (marcar como deleted, borrar de verdad después de N días, permitir cancelar)?
6. **Account deletion — qué pasa con data derivada compartida**: mangas y chapters viven compartidos entre users (ADR 0004 normalización). Al borrar al user, NO borramos esos. Solo desaparece la library_entry. Confirmar.
7. **Audit log**: ¿guardamos en algún lado "user X borró su cuenta el día Y" para soporte? Tradeoff con "right to be forgotten" pleno.

**Recomendaciones tentativas:**

1. **Formato**: JSON único. Es lo más completo, más fiel a la estructura interna, y un user técnico puede convertirlo a CSV fácil. CSV pierde info (relaciones).
2. **Scope**: todo lo que sea del user: perfil, settings, library_entries (con título del manga y URL, no copias del cover image), last_read_chapter_id resolved a número, cap leídos en formato legible.
3. **Entrega**: descarga directa desde `/settings/export`. JSON pesa poco aunque tengas 1000 entries (~200KB sin minify). No necesitamos cola.
4. **Confirmación**: re-OTP por email. Mandamos código a su email, el user lo tipea en `/settings/delete-account`. Coherente con el resto del auth flow (OTP). Cero código nuevo aparte del handler.
5. **Alcance**: **hard delete** después de un período de gracia de 7 días (marcado como `deleted_at`, ningún login posible, no aparece en públicos). Si el user vuelve a hacer login con su email durante esos 7 días, mostramos "tu cuenta está pendiente de borrado, ¿cancelar?". Después de 7 días, cron borra hard. Razón: la gente se arrepiente. 7 días balancea "right to erasure" (sí se borra) con "user puede deshacer un click impulsivo".
6. **Data compartida**: confirmado, no borramos mangas/chapters. Sí borramos: user, library_entries del user, sessions, login_codes, unsubscribe tokens.
7. **Audit log**: una fila mínima en tabla `deletion_log(user_id_hash, deleted_at)` donde `user_id_hash` es un hash one-way. No es PII recuperable. Suficiente para soporte ("borré mi cuenta hace un mes, ¿la pueden restaurar?" → "no, fue hard delete hace 23 días").

> **Tu respuesta:**
> Listo, estoy de acuerdo con las recomendaciones.

---

## Decidido — resumen ejecutivo

Las 11 dimensiones cerraron. Detalle de cada una en [ADR 0013](adr/0013-fase-2-release-tracking.md). Lo esencial:

| Dim | Decisión final | Razón |
|-----|----------------|-------|
| 1   | `/releases` página + widget home + email semanal adaptativo + endpoint admin para test + mailpit en dev | Cubre las dos razones del producto sin sobreingeniería |
| 2   | `/releases` 100% privada, sin métricas sociales | Minimiza superficie de privacy |
| 3   | Matching híbrido (auto si confianza alta, manual si ambigüedad) + corregible desde UI | Sin falsos positivos silenciosos |
| 4   | Mangas/chapters compartidos + cursor por entry (sin tabla `chapter_reads`) | Esquema simple y normalizado |
| 5   | Goroutine in-process, tick 1h, `next_check_at` por manga, filtro `EXISTS reading` | Sin binario aparte, sin tabla de jobs |
| 6   | Multipart plain + HTML básico, opt-in default ON en signup, List-Unsubscribe one-click | Adopción alta + compliance GDPR/LGPD |
| 7   | Guardamos todos los caps históricos + checkbox "marcar todos leídos" al agregar manga | Flexibilidad sin costo + UX natural |
| 8   | Filtro por idioma preferido (`en` default, `es` opcional) | Evita duplicados cross-lingual |
| 9   | Cursor `last_read_chapter_id` (FK a chapters), sin tabla granular | Cubre 95% del flujo (lectura lineal) |
| 10  | Sin abstracción de sources — paquete único `internal/mangadex` | YAGNI |
| 11  | Export JSON al vuelo + soft delete 7d + re-OTP + hash audit log | File-over-app + GDPR Art. 15/17 |

Modifica ADR 0007 (email transport): auth emails siguen plain, product emails pasan a multipart plain+HTML básico. Anotar en 0007.

---

## Próximos pasos

1. ✅ ADR 0013 escrito y aceptado.
2. Plan de commits en branch `fase-2-release-tracking` (este branch):
   1. **Migration**: `migrations/0004_release_tracking.sql` con columnas en `mangas`, `library_entries`, `users` + tablas `chapters`, `deletion_log`. (Schema completo en ADR 0013 §4.)
   2. **Cliente MangaDex**: paquete `internal/mangadex` con cliente HTTP, tests contra fixtures de respuestas reales.
   3. **Matching híbrido**: integrar al flujo de `library/add` — buscar en MangaDex al agregar, autolinkear si score alto, marcar `needs_review` si no. UI nueva en `/library/entry/{id}` para corregir match manualmente.
   4. **Worker periódico**: goroutine en `main.go` + paquete `internal/releases/worker` con `time.Ticker`, query de mangas a chequear, persist de caps nuevos, backoff.
   5. **Página `/releases`**: handler en `internal/releases` (paquete nuevo o dentro de `library`) listando caps no leídos por user. Widget "últimos releases" en home.
   6. **Cursor de lectura**: UI en `/releases` y `/library/entry/{id}` para "marcar leído hasta este cap". Actualiza `library_entries.last_read_chapter_id`.
   7. **Setting de email digest**: toggle en `/settings` + checkbox default ON en signup (`/auth/start` o paso posterior).
   8. **Endpoint admin de prueba**: `POST /admin/trigger-digest?user=<email>` autenticado vía `ADMIN_TOKEN` env var.
   9. **Worker de digest semanal**: cron interno domingo 9am UTC, query de caps nuevos por user opt-in, render plain + HTML, mandar vía cliente SMTP existente (`internal/email`) con headers `List-Unsubscribe`.
   10. **Unsubscribe endpoint**: `POST /unsubscribe` con token HMAC, marca `email_digest_enabled = 0`. Headers en cada email saliente.
   11. **Export de data**: `GET /settings/export` que serializa el user a JSON y devuelve como descarga.
   12. **Account deletion**: `POST /settings/delete-account/request` + `/confirm` con re-OTP. Soft delete + cron diario para hard delete después de 7 días.
   13. **Actualizar ADR 0007**: nota sobre product emails multipart vs auth emails plain.
   14. **Actualizar docs**: [estado.md](estado.md), [decisiones-pendientes.md](decisiones-pendientes.md), `.env.example` (nuevas env vars: `ADMIN_TOKEN`, `UNSUBSCRIBE_SECRET`, `DELETION_LOG_PEPPER`).
   15. **Bump `VERSION`** a `v0.2.0` antes de mergear a `main` (release feature-mayor visible al usuario).
3. PR `fase-2-release-tracking` → `main` cuando esté todo verde. CD deploya automático.
4. Mover este archivo a `docs/archivo/diseno-fase-2.md` o eliminarlo (decisión post-merge).
