-- 0014_source_backoff.sql
--
-- Backoff persistente por (manga, source) + índice para la consulta que el
-- worker corre en cada poll.
--
-- El problema que resuelve consecutive_failures: hasta ahora, cuando un
-- poll fallaba con algo que no fuera rate limit, el worker registraba el
-- error y volvía SIN reagendar. next_check_at se quedaba como estaba — y
-- para una fila recién matcheada eso es NULL. Como la cola ordena
-- `next_check_at IS NULL DESC, next_check_at ASC`, esa fila quedaba en la
-- primera posición de todos los batches, para siempre. Una serie borrada
-- upstream (404) o una source con el HTML cambiado se convertía en una
-- fila envenenada que se reintentaba cada tick y le robaba el turno a las
-- series sanas. Con BatchSize=50, cincuenta de esas y nada más se polleaba.
--
-- Ahora cada fallo incrementa el contador y agenda con backoff exponencial,
-- así que una fila muerta se degrada sola hasta costar un slot por día en
-- vez de uno por hora. Un éxito lo resetea a 0.
--
-- last_error / last_error_at duplican en DB lo que SourceHealth guarda en
-- memoria. La duplicación es a propósito: el estado in-memory se pierde en
-- cada restart, y mokuji deploya seguido, así que una degradación lenta era
-- invisible. En DB sobrevive y se puede consultar por fila, no solo por
-- source.

ALTER TABLE manga_sources ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE manga_sources ADD COLUMN last_error TEXT;
ALTER TABLE manga_sources ADD COLUMN last_error_at INTEGER;

-- latestPublishedAt hace MAX(published_at) WHERE manga_id = ? AND source = ?
-- una vez por cada poll de cada serie. El índice que ya existía
-- (idx_chapter_manga_published) cubre manga_id pero no source, así que
-- SQLite acotaba por manga y después escaneaba filtrando. Con source en el
-- índice es un seek directo al máximo.
--
-- El índice viejo se conserva: lo usan las queries de /releases y del digest,
-- que filtran por manga sin importar la source.
CREATE INDEX idx_chapter_manga_source_published
    ON chapter(manga_id, source, published_at DESC);
