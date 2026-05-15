# ADR 0013: Fase 2 — Release tracking (diseño completo)

- **Estado**: Aceptada
- **Fecha**: 2026-05-15

## Contexto

Fase 1 de mokuji dejó la app utilizable como tracker manual: el usuario marca capítulos leídos cuando los lee, busca mangas en AniList, edita su biblioteca. El loop no está cerrado: para saber si hay capítulo nuevo, el usuario tiene que ir a MangaDex o a un sitio de scanlator a chequear.

Fase 2 cierra ese loop. La app pollea releases de los mangas en `status=reading` del usuario y le avisa cuando hay capítulo nuevo, vía página `/releases` in-app y email digest semanal. Como secundario obligatorio (por filosofía Odin y por compliance GDPR/LGPD), también entra **export + delete de la data del usuario**.

Diseño completo destilado de [docs/diseno-fase-2.md](../diseno-fase-2.md), que documenta el proceso de discusión por dimensiones, alternativas evaluadas, y respuestas del owner del producto.

## Decisión

Fase 2 incluye 11 áreas de decisión. Cada subsección abajo es una decisión cerrada con su razón concisa.

### 1. Scope del MVP

- Superficie principal in-app: página `/releases` listando capítulos nuevos de mangas con `status=reading`. Widget "últimos releases" en home cuando hay sesión, que linkea a `/releases`.
- Email digest semanal con cadencia fija (domingo 9am UTC). Adaptativo: skip si no hay capítulos nuevos esa semana.
- Cobertura: **solo `status=reading`**. Mangas en `planned`, `completed`, o sin status no entran al worker ni al digest.
- Badge in-app con contador de no leídos: **post-MVP**.
- **Mecanismo de prueba de email**:
  - Mailpit cubre el flow automático en dev (el digest semanal igual se manda a mailpit en dev cuando el cron tickee).
  - Endpoint admin `POST /admin/trigger-digest?user=<email>` autenticado vía header `Authorization: Bearer <ADMIN_TOKEN>` (env var). Dispara el digest *ahora* para iterar templates y para QA post-deploy.

**Razón.** Cubre las dos razones del producto (cerrar el loop in-app + email proactivo) sin meter ejes configurables que se pueden agregar después. Badge se posterga porque agrega complejidad de UI (estado "no leído" por sesión, persistencia entre devices) sin valor crítico al MVP.

### 2. Visibility / privacy

- `/releases` es siempre privada — solo el dueño autenticado la ve. Sin variante pública `/u/{username}/releases`.
- Sin métricas agregadas expuestas ("X usuarios siguen este manga", etc.). Sin features sociales.
- Si la lista del usuario es privada, los releases siguen funcionando para él (obvio).

**Razón.** Minimiza superficie de privacy y simplifica controladores. No cierra la puerta a features sociales en el futuro, solo no entran en fase 2.

### 3. Matching AniList → MangaDex

- Estrategia **híbrida**:
  - Al agregar un manga a la library (o lazy en el primer poll), buscar en MangaDex por título (romaji + inglés según AniList).
  - Si hay **un solo resultado con score alto** (título idéntico o muy similar), autolinkear silenciosamente y guardar `mangadex_id`.
  - Si hay **ambigüedad** (varios candidatos parecidos) o **nada** matchea bien, marcar `mangadex_status='needs_review'` y mostrar al usuario en `/library/entry/{id}` un selector para confirmar manualmente.
  - El usuario siempre puede ir a `/library/entry/{id}` y decir "este match está mal" aun cuando haya sido automático.
- **No** usamos dumps comunitarios de cross-reference (manga-tracker-bot etc.) como dependencia. Sí podemos consultarlos como hint si la búsqueda por título falla mucho en la práctica.

**Razón.** Automático puro tiene falsos positivos silenciosos peligrosos (mostrar caps del manga equivocado). Manual puro agrega fricción en el 90% de los casos donde el match es trivial. Híbrido captura la simplicidad cuando hay confianza y delega al usuario cuando no.

### 4. Storage / data model

Migración nueva (`migrations/0004_release_tracking.sql` tentativo):

```sql
-- Vínculo AniList ↔ MangaDex (compartido entre users)
ALTER TABLE mangas ADD COLUMN mangadex_id TEXT;
ALTER TABLE mangas ADD COLUMN mangadex_status TEXT
    DEFAULT 'unmatched'
    CHECK (mangadex_status IN ('unmatched','matched','needs_review','no_results'));
ALTER TABLE mangas ADD COLUMN mangadex_checked_at TIMESTAMP;
ALTER TABLE mangas ADD COLUMN mangadex_next_check_at TIMESTAMP;
CREATE INDEX idx_mangas_mangadex_next_check
    ON mangas(mangadex_next_check_at)
    WHERE mangadex_id IS NOT NULL;

-- Capítulos del manga (compartido entre users)
CREATE TABLE chapters (
    id INTEGER PRIMARY KEY,
    manga_id INTEGER NOT NULL REFERENCES mangas(id),
    source TEXT NOT NULL,        -- 'mangadex' por ahora
    source_id TEXT NOT NULL,     -- UUID del cap en MangaDex
    number TEXT,                 -- '125', '125.5', 'Special' (TEXT por flexibilidad)
    title TEXT,
    language TEXT,               -- 'en', 'es', ...
    published_at TIMESTAMP,
    external_url TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT (datetime('now')),
    UNIQUE(source, source_id)
);
CREATE INDEX idx_chapters_manga_published
    ON chapters(manga_id, published_at DESC);

-- Cursor de lectura del usuario por entry
-- (ver Decisión 9: cursor en lugar de tabla chapter_reads granular)
ALTER TABLE library_entries
    ADD COLUMN last_read_chapter_id INTEGER REFERENCES chapters(id);

-- Setting de email digest (default ON, ver Decisión 6)
ALTER TABLE users ADD COLUMN email_digest_enabled INTEGER NOT NULL DEFAULT 1;

-- Soft-delete con período de gracia (ver Decisión 11)
ALTER TABLE users ADD COLUMN deleted_at TIMESTAMP;

-- Log de borrado (ver Decisión 11)
CREATE TABLE deletion_log (
    user_id_hash TEXT PRIMARY KEY,  -- SHA-256(user_id || pepper)
    deleted_at TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);

-- Tokens de unsubscribe / one-click (ver Decisión 6)
CREATE TABLE unsubscribe_tokens (
    token TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id),
    scope TEXT NOT NULL,         -- 'releases_digest'
    created_at TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);
```

**Razón.** Mangas y chapters normalizados (uno por manga, compartido entre users) sigue la línea de ADR de fase 1. Idioma como columna en chapters permite filtrar por preferencia del usuario sin tabla aparte. `mangadex_next_check_at` permite scheduling por manga sin tabla de cola. Indices cubren las queries del worker (`WHERE next_check_at < now`) y de listado (`WHERE manga_id = X ORDER BY published_at DESC`).

### 5. Worker design

- **Goroutine única** dentro del proceso de mokuji (no binario separado).
- Schedule con `time.Ticker` cada 1h.
- En cada tick:
  ```sql
  SELECT id, mangadex_id FROM mangas
  WHERE mangadex_id IS NOT NULL
    AND (mangadex_next_check_at IS NULL OR mangadex_next_check_at < now())
    AND EXISTS (
      SELECT 1 FROM library_entries
      WHERE manga_id = mangas.id AND status = 'reading'
    )
  ORDER BY mangadex_next_check_at NULLS FIRST
  LIMIT 50;
  ```
- Por cada manga: 1 request a `/manga/{id}/feed?translatedLanguage[]=en&order[publishAt]=asc&limit=100` con backoff exponencial en 429/5xx. Persistir caps nuevos. Setear `mangadex_next_check_at = now + 6h`.
- Concurrencia: **1 manga a la vez** dentro de la goroutine. MangaDex sugiere rate limit conservador (5 req/seg global por IP); con 1/seg sostenido cubrimos hasta 600 mangas/hora sin acercarnos al límite.
- Sin checkpoint complejo: si el binario reinicia mid-poll, el siguiente tick toma desde donde quedó (ya hay `next_check_at` persistido).

**Razón.** Goroutine in-process es lo más simple coherente con el resto del stack (un solo binario, un solo proceso). Cola implícita via `next_check_at` en la tabla `mangas` evita necesidad de una tabla de jobs. Filtro `EXISTS reading` evita pollear mangas que nadie lee.

### 6. Notificación

#### 6.1 Cadencia y trigger

- Email digest **semanal fijo**, domingo 9am UTC.
- **Adaptativo**: si no hay capítulos nuevos esa semana para un usuario, no se le manda email (silencio mejor que "nada nuevo").

#### 6.2 Formato

- **Multipart plain + HTML básico** (RFC 2046).
- Plain-text estructurado como fallback (clientes que no renderizan HTML).
- HTML básico = `<h2>`, `<ul>`, `<li>`, `<a>`. Sin CSS inline, sin imágenes, sin layout complejo. Compatible con todos los clientes (Outlook, Gmail dark mode, etc.).
- Contenido por cap: título del manga, número/título del cap, fecha publicación, link al reader de MangaDex.
- Cuando entre marca/diseño visual (decisión pendiente del backlog general), evolucionar a HTML pleno con CSS inline. Esto **modifica el alcance de ADR 0007** (que decía "plain-text inicial"): mantiene plain-text para auth emails, pero permite HTML básico multipart para emails de producto. Anotar este shift en ADR 0007 con nota.

#### 6.3 Opt-in

- **Opción A**: opt-in al signup con checkbox **default ON visible**. En la página `/auth` (o paso posterior si fuera necesario), checkbox "Recibir email semanal con releases de mi biblioteca" pre-tildado.
- El usuario lo ve y puede destildar antes de crear la cuenta. Cumple "freely given, specific, informed and unambiguous" de GDPR Art. 7 porque el consentimiento es explícito.
- Toggle en `/settings` para cambiar en cualquier momento (ya hay ahí toggle de visibility, agregamos uno más).

#### 6.4 Unsubscribe

- Headers RFC 8058 (one-click) + RFC 2369 en cada email saliente:
  ```
  List-Unsubscribe: <https://mokuji.org/unsubscribe?token=...>, <mailto:unsub@mokuji.org?subject=...>
  List-Unsubscribe-Post: List-Unsubscribe=One-Click
  ```
- Token = HMAC-SHA256 firmado server-side con secret (env var), codifica `user_id + scope=releases_digest + created_at`. Validado en el endpoint, marca `email_digest_enabled = 0`. Sin sesión necesaria — el token es la auth.
- Endpoint `POST /unsubscribe` (sin auth de sesión, valida token). Gmail/Apple Mail muestran "Darse de baja" arriba del email con estos headers.
- Tabla `unsubscribe_tokens` opcional para auditoría si quisiéramos invalidar tokens viejos, pero con HMAC firmado y `created_at` embebido podemos rechazar tokens con `age > 90 días` sin storage. Empezamos sin tabla; la agregamos si aparece necesidad.

#### 6.5 Badge in-app

- Contador numérico junto a "biblioteca" en el nav.
- **Post-MVP**. No bloquea fase 2.

**Razón.** Adaptativo evita "weekly spam" cuando no hay nada que decir. Multipart cubre todos los clientes con HTML moderno + fallback. Default ON con consentimiento explícito al signup balancea adopción alta con cumplimiento legal (GDPR/LGPD/CAN-SPAM). List-Unsubscribe es estándar y aceptado universalmente.

### 7. Histórico

- Guardamos **todos** los capítulos publicados de un manga matcheado en MangaDex (opción (a) del doc de diseño).
- Primer poll por manga: pagina la API de MangaDex (`limit=100`, iterar `offset`) hasta traer todos los caps. Cota práctica: mangas con 500+ caps son raros y se manejan con paginación.
- **Sin podar** capítulos viejos por ahora. SQLite escala bien — un manga con 500 caps son 500 filas.
- Al agregar un manga a la library, **checkbox "Marcar todos los capítulos publicados como leídos"**:
  - Default tildado si `status=reading` (asunción: si lo agregás como reading, lo venías leyendo).
  - Default destildado si `status=planned`.
  - Implementación: setear `library_entries.last_read_chapter_id` al cap más reciente conocido al momento del add.

**Razón.** Persistir todo da flexibilidad (mostrar "leíste 12/45", historial, etc.) sin costo real. El checkbox "ya leí todos" cubre el caso natural del usuario que ya viene leyendo el manga desde antes.

### 8. Idioma / scanlation

- Filtro **por idioma preferido del usuario** (default `en`).
- Setting nuevo en `/settings`: `preferred_language` con valores reducidos: `en`, `es`. Agregar más idiomas cuando alguien los pida.
- Si una versión en el idioma preferido no existe para un cap, no lo mostramos (mejor silencio que ruido cross-lingual).
- **Sin filtro por scanlation group**. Overkill para el MVP.

**Razón.** MangaDex tiene múltiples versiones del mismo cap (varios grupos, varios idiomas). Filtrar por idioma evita duplicados y respeta lo que el usuario realmente lee. Group filter agrega UX compleja sin valor proporcional.

### 9. Marcar como leído (cursor)

- **Cursor por chapter_id**, no tabla `chapter_reads` granular.
- Columna nueva: `library_entries.last_read_chapter_id INTEGER REFERENCES chapters(id)`.
- "Caps no leídos del user X en manga M":
  ```sql
  SELECT * FROM chapters
  WHERE manga_id = M
    AND published_at > (
      SELECT published_at FROM chapters WHERE id = (
        SELECT last_read_chapter_id FROM library_entries
        WHERE user_id = X AND manga_id = M
      )
    );
  ```
- UI: en `/releases` y en `/library/entry/{id}`, botón "marcar como leído hasta este cap" que actualiza `last_read_chapter_id` al cap clickeado.

**Razón (con pushback del owner del producto y aceptación).** La mayoría lee lineal; granular es over-engineering. Cursor por `chapter_id` (no por número) evita problemas de parsing con caps "8.5"/"Special" — ordenamos por `published_at` que siempre existe y es monotónico.

### 10. Abstracción para sources futuros

- **Sin abstracción** en fase 2. Paquete único `internal/mangadex` hardcoded.
- Si en el futuro aparece un segundo provider (Mangaplus, oficiales, etc.), refactorizar a `internal/releasesource` con interface `Source`. Costo bajo porque el worker es el único caller.

**Razón.** YAGNI. La interface `mangasource.Source` de fase 1 sirvió porque AniList ya tenía buena chance de ganar competencia (Kitsu, MAL, etc.). Para releases, MangaDex es claramente dominante en scanlation y no hay candidato segundo cercano.

### 11. Data export + account deletion (must-have del MVP)

#### 11.1 Export

- Endpoint `GET /settings/export` (autenticado, devuelve archivo JSON).
- Formato JSON único. Contenido: perfil, settings, library_entries con título de manga + URL + `last_read_chapter_id` resuelto a número y fecha, capítulos relevantes (los que están en library del user), tokens de unsubscribe.
- Sin cola de jobs — JSON pesa poco (~200KB con 1000 entries), se genera al vuelo.

#### 11.2 Account deletion

- Confirmación: **re-OTP por email**. Endpoint `POST /settings/delete-account/request` manda código al email del user. Endpoint `POST /settings/delete-account/confirm` valida código + marca `users.deleted_at = now()`.
- **Soft delete con gracia de 7 días**:
  - Mientras `deleted_at IS NOT NULL` y `deleted_at + 7 días > now()`: el usuario no puede loguear normalmente, pero un intento de login muestra "tu cuenta está pendiente de borrado, ¿cancelar?" con un botón que setea `deleted_at = NULL`.
  - Cron diario (puede ser otra goroutine en el mismo binario) hace hard delete de users con `deleted_at + 7 días < now()`.
- **Hard delete**: borra `users`, `library_entries` del user, `sessions`, `login_codes`, `unsubscribe_tokens`. **No** borra `mangas` ni `chapters` (data compartida).
- **Audit log mínimo**: tabla `deletion_log(user_id_hash, deleted_at)`. `user_id_hash = SHA-256(user_id || pepper)` donde `pepper` es env var. No es PII recuperable; suficiente para responder a soporte tipo "borré mi cuenta hace un mes, ¿la restauran?" → "no, fue hard delete hace 23 días".

**Razón.** Filosofía Odin (el usuario es dueño de sus datos) + compliance GDPR/LGPD. Soft delete con gracia balancea "right to erasure" con la realidad de que la gente se arrepiente de clicks impulsivos. Hash de audit log permite trazabilidad sin retener PII.

## Alternativas consideradas (resumen)

Detalle completo en [docs/diseno-fase-2.md](../diseno-fase-2.md). Las que más costaron:

- **Matching automático puro** (Dim 3): rechazado por falsos positivos silenciosos. Mostrar caps del manga equivocado erosiona la confianza del usuario sin que se entere.
- **Default OFF para email digest** (Dim 6): rechazado por adopción ~0%. Default ON sin consentimiento explícito rechazado por GDPR. Elegida la solución (A) con checkbox visible al signup.
- **Tabla `chapter_reads` granular** (Dim 9): rechazada por over-engineering. Cursor cubre el caso lineal real.
- **Worker en binario separado** (Dim 5): rechazado por complejidad operativa (otro systemd unit, otro deploy path, IPC con la DB). In-process goroutine es trivial.
- **Hard delete inmediato sin gracia** (Dim 11): rechazado por arrepentimiento de usuarios. 7 días balancea derecho a borrar con UX humana.
- **Abstracción de sources desde día 1** (Dim 10): rechazado por YAGNI. MangaDex es dominante y el costo de refactor futuro es bajo.

## Consecuencias

### Más fácil

- Usuario abre mokuji.org en domingo y ve el digest sin haber configurado nada (default ON al signup).
- "Cerrar el loop" del producto: ya no hay que ir afuera para ver si hay caps nuevos.
- Onboarding más fluido — el checkbox "marcar todos como leídos" al agregar manga cubre el caso natural.
- Cumplimiento GDPR/LGPD funcional desde día uno (export + delete).
- Self-hosters pueden ofrecer la app a sus usuarios con email digest funcionando (Resend o cualquier SMTP con port alternativo).

### Más difícil

- ADR 0007 (email transport plain-text) queda parcialmente revisitado — auth emails siguen plain, product emails pasan a multipart. Hay que anotar el cambio en ADR 0007.
- Worker periódico introduce primer side-effect background del binario (hasta ahora solo session cleanup, que era trivial). Si el worker se cuelga, los digests no salen. Health check post-deploy tendría que verificarlo eventualmente (no en MVP).
- Matching híbrido requiere UI nueva en `/library/entry/{id}` para corregir manualmente. No es complejo pero suma surface.
- Soft delete con gracia agrega lógica de "cuenta pendiente de borrado" en el flow de auth.
- DB crece más rápido — mangas con 500 caps × N usuarios trackeando = más rows. Sigue manejable, pero monitorear.

### Para self-hosters

Sin cambios al contrato de install — sigue siendo binario + systemd + nginx + Resend SMTP. Agregar al `.env.example`:
- `ADMIN_TOKEN` (para el endpoint de prueba de digest).
- `UNSUBSCRIBE_SECRET` (para HMAC de tokens).
- `DELETION_LOG_PEPPER` (para hash one-way del audit log).

## Regla de evolución

Revisitar fase 2 cuando:

- Aparezca un segundo provider de releases (Mangaplus oficial, etc.) — refactorizar a interface `releasesource.Source`.
- La búsqueda por título falle en >10% de los matches automáticos — considerar dumps comunitarios como hint.
- Mailpit + endpoint admin no alcancen para QA de templates — armar entorno staging dedicado.
- DB pase los 100MB o queries de releases se degraden — considerar podar caps con >2 años o vacuum periódico.
- Aparezcan features sociales (feed público, etc.) — revisitar visibility de `/releases` (Dim 2).
- Llegue feedback consistente sobre granularidad de "marcado como leído" — considerar tabla `chapter_reads` opcional.
