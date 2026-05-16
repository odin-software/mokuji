// Package mangadex implementa un cliente HTTP puro para la API pública
// REST de MangaDex (https://api.mangadex.org).
//
// Sin auth (los endpoints de lectura son públicos). Rate limit conservador
// cliente-side (2 req/s sostenido, burst 1) para mantenernos bien por
// debajo del límite real (~5 req/s por IP). Si la API igual responde 429
// — porque otro proceso comparte la IP, o porque MangaDex apretó
// temporalmente — respetamos el header Retry-After con 1 retry; si
// persiste, devolvemos ErrRateLimited para que el caller (worker) reagende.
package mangadex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"git.odin.do/kengru/mokuji/internal/releasesource"
)

const (
	defaultEndpoint     = "https://api.mangadex.org"
	defaultTimeout      = 15 * time.Second
	defaultRateInterval = 500 * time.Millisecond // 2 req/s sostenido
	defaultRateBurst    = 1
	defaultUserAgent    = "mokuji/0.x (+https://mokuji.org)"

	maxSearchResults = 10
	feedPageLimit    = 100
	feedPageHardCap  = 10000 // defensa contra loops si el API devuelve respuestas raras

	maxRetryAfterDelay = 10 * time.Second // si Retry-After excede esto, abortamos
	defaultRetryDelay  = 2 * time.Second
)

// ErrRateLimited y ErrNotFound son alias hacia el paquete genérico
// releasesource para que callers que importaban estos sentinels (worker,
// matcher) sigan comparando con la misma identidad de error. La fuente
// real vive en releasesource — el sentinel cliente-side es solo un
// re-export. errors.Is sigue funcionando porque es la misma var.
var (
	ErrRateLimited = releasesource.ErrRateLimited
	ErrNotFound    = releasesource.ErrNotFound
)

// Client habla con api.mangadex.org.
type Client struct {
	endpoint  string
	http      *http.Client
	rl        *rate.Limiter
	userAgent string
	sleeper   func(context.Context, time.Duration)
}

// Option configura un Client.
type Option func(*Client)

// WithEndpoint override del endpoint base (útil para tests con httptest).
func WithEndpoint(u string) Option { return func(c *Client) { c.endpoint = u } }

// WithHTTPClient override del *http.Client (para tests o transport custom).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithRateLimit override del rate limiter cliente-side.
func WithRateLimit(every time.Duration, burst int) Option {
	return func(c *Client) { c.rl = rate.NewLimiter(rate.Every(every), burst) }
}

// WithUserAgent override del header User-Agent.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// WithSleeper override de la función que duerme entre retries. En tests
// se inyecta una versión que no espera tiempo real.
func WithSleeper(f func(context.Context, time.Duration)) Option {
	return func(c *Client) { c.sleeper = f }
}

// New construye un Client con defaults razonables para producción.
func New(opts ...Option) *Client {
	c := &Client{
		endpoint:  defaultEndpoint,
		http:      &http.Client{Timeout: defaultTimeout},
		rl:        rate.NewLimiter(rate.Every(defaultRateInterval), defaultRateBurst),
		userAgent: defaultUserAgent,
		sleeper:   ctxSleep,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Name retorna el identificador estable de la source. Cumple la
// interfaz releasesource.Client; usado como key del map de clientes
// en el worker y como valor de manga_sources.source.
func (c *Client) Name() string { return "mangadex" }

// Search busca manga por título. Devuelve hasta maxSearchResults
// candidatos ordenados por relevancia según MangaDex. Filtra contenido
// pornográfico (mokuji es público sin gate adulto).
func (c *Client) Search(ctx context.Context, title string) ([]MangaResult, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, nil
	}

	q := url.Values{}
	q.Set("title", title)
	q.Set("limit", strconv.Itoa(maxSearchResults))
	q.Add("includes[]", "cover_art")
	q.Add("contentRating[]", "safe")
	q.Add("contentRating[]", "suggestive")
	q.Add("contentRating[]", "erotica")

	var resp searchResponse
	if err := c.doGET(ctx, "/manga", q, &resp); err != nil {
		return nil, err
	}

	out := make([]MangaResult, 0, len(resp.Data))
	for _, m := range resp.Data {
		out = append(out, m.toResult())
	}
	return out, nil
}

// FetchChapters devuelve los capítulos de un manga en el idioma dado.
// Si since no es zero, filtra a capítulos publicados después de esa fecha.
// Maneja paginación (API es limit/offset, máx 100 por página) y ordena
// por publishAt ascendente para que el caller pueda procesar cronológicamente.
// Cumple releasesource.Client.FetchChapters.
func (c *Client) FetchChapters(ctx context.Context, mangaID, language string, since time.Time) ([]Chapter, error) {
	if mangaID == "" {
		return nil, errors.New("mangadex: mangaID requerido")
	}
	if language == "" {
		language = "en"
	}

	var all []Chapter
	offset := 0
	for {
		q := url.Values{}
		q.Add("translatedLanguage[]", language)
		q.Set("limit", strconv.Itoa(feedPageLimit))
		q.Set("offset", strconv.Itoa(offset))
		q.Set("order[publishAt]", "asc")
		q.Set("includeFuturePublishAt", "0")
		// NO seteamos includeEmptyPages=0: muchos mangas oficiales (One Piece,
		// One Punch Man, Spy x Family, etc.) publican via plataforma externa
		// (MangaPlus, etc.) y MangaDex sólo mantiene el meta + externalUrl.
		// Esos caps están marcados como "empty pages" en MangaDex y serían
		// excluidos con el filtro, dejando esos mangas con 0 caps visibles.
		// El default del API es incluir empty pages — lo dejamos así.
		if !since.IsZero() {
			q.Set("publishAtSince", since.UTC().Format("2006-01-02T15:04:05"))
		}

		var resp feedResponse
		if err := c.doGET(ctx, "/manga/"+mangaID+"/feed", q, &resp); err != nil {
			return nil, err
		}

		for _, ch := range resp.Data {
			converted, ok := ch.toChapter()
			if !ok {
				continue
			}
			all = append(all, converted)
		}

		offset += len(resp.Data)
		if len(resp.Data) == 0 || offset >= resp.Total || offset >= feedPageHardCap {
			break
		}
	}

	return all, nil
}

// doGET ejecuta un GET contra el endpoint con rate limiting preventivo
// y manejo de 429/5xx con un retry.
func (c *Client) doGET(ctx context.Context, path string, q url.Values, out any) error {
	fullURL := c.endpoint + path
	if len(q) > 0 {
		fullURL += "?" + q.Encode()
	}

	var lastErr error
	for attempt := range 2 {
		if err := c.rl.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
		if err != nil {
			return fmt.Errorf("crear request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("http: %w", err)
			// Errores de red transitorios: 1 retry con backoff corto.
			if attempt == 0 {
				c.sleeper(ctx, defaultRetryDelay+jitter())
				continue
			}
			return lastErr
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("leer respuesta: %w", err)
			}
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("parse json: %w", err)
			}
			return nil

		case resp.StatusCode == http.StatusTooManyRequests:
			delay := parseRetryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			if delay > maxRetryAfterDelay {
				return ErrRateLimited
			}
			if delay <= 0 {
				delay = defaultRetryDelay
			}
			if attempt == 0 {
				c.sleeper(ctx, delay)
				lastErr = ErrRateLimited
				continue
			}
			return ErrRateLimited

		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return ErrNotFound

		case resp.StatusCode >= 500 && resp.StatusCode < 600:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
			resp.Body.Close()
			lastErr = fmt.Errorf("mangadex status %d: %s", resp.StatusCode, truncate(string(body), 200))
			if attempt == 0 {
				c.sleeper(ctx, defaultRetryDelay+jitter())
				continue
			}
			return lastErr

		default:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
			resp.Body.Close()
			return fmt.Errorf("mangadex status %d: %s", resp.StatusCode, truncate(string(body), 200))
		}
	}

	if lastErr != nil {
		return lastErr
	}
	return errors.New("mangadex: retries agotados")
}

// parseRetryAfter parsea el header Retry-After. Soporta segundos (entero)
// y HTTP-date (RFC 7231). Si no puede parsear, devuelve 0.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// ctxSleep duerme d, pero abandona si el contexto se cancela.
func ctxSleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// jitter agrega aleatoriedad ±500ms al backoff para evitar thundering herd.
func jitter() time.Duration {
	return time.Duration(rand.Int64N(int64(time.Second)))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- Tipos internos de parsing (no exportados) ---

type searchResponse struct {
	Result string        `json:"result"`
	Data   []mangaEntity `json:"data"`
	Total  int           `json:"total"`
}

type mangaEntity struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Attributes struct {
		Title     map[string]string   `json:"title"`
		AltTitles []map[string]string `json:"altTitles"`
		Year      int                 `json:"year"`
		Status    string              `json:"status"`
	} `json:"attributes"`
	Relationships []relationship `json:"relationships"`
}

type relationship struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Attributes json.RawMessage `json:"attributes,omitempty"`
}

type coverArtAttributes struct {
	FileName string `json:"fileName"`
}

func (m mangaEntity) toResult() MangaResult {
	r := MangaResult{
		ID:     m.ID,
		Year:   m.Attributes.Year,
		Status: m.Attributes.Status,
	}

	if t := m.Attributes.Title["en"]; t != "" {
		r.Title = t
	} else {
		// Cualquier idioma del título principal, en orden estable de las keys.
		for _, lang := range sortedKeys(m.Attributes.Title) {
			if v := m.Attributes.Title[lang]; v != "" {
				r.Title = v
				break
			}
		}
	}

	for _, lang := range sortedKeys(m.Attributes.Title) {
		if v := m.Attributes.Title[lang]; v != "" {
			r.Titles = append(r.Titles, v)
		}
	}
	for _, alt := range m.Attributes.AltTitles {
		for _, lang := range sortedKeys(alt) {
			if v := alt[lang]; v != "" {
				r.Titles = append(r.Titles, v)
			}
		}
	}

	for _, rel := range m.Relationships {
		if rel.Type != "cover_art" || len(rel.Attributes) == 0 {
			continue
		}
		var ca coverArtAttributes
		if err := json.Unmarshal(rel.Attributes, &ca); err == nil && ca.FileName != "" {
			r.CoverURL = "https://uploads.mangadex.org/covers/" + m.ID + "/" + ca.FileName
			break
		}
	}

	return r
}

type feedResponse struct {
	Result string          `json:"result"`
	Data   []chapterEntity `json:"data"`
	Total  int             `json:"total"`
}

type chapterEntity struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Attributes struct {
		Title              string `json:"title"`
		Chapter            string `json:"chapter"`
		TranslatedLanguage string `json:"translatedLanguage"`
		ExternalURL        string `json:"externalUrl"`
		PublishAt          string `json:"publishAt"`
	} `json:"attributes"`
}

func (e chapterEntity) toChapter() (Chapter, bool) {
	if e.Attributes.PublishAt == "" {
		return Chapter{}, false
	}
	t, err := time.Parse(time.RFC3339, e.Attributes.PublishAt)
	if err != nil {
		return Chapter{}, false
	}
	return Chapter{
		ID:          e.ID,
		Number:      e.Attributes.Chapter,
		Title:       e.Attributes.Title,
		Language:    e.Attributes.TranslatedLanguage,
		PublishedAt: t,
		ExternalURL: e.Attributes.ExternalURL,
	}, true
}

// sortedKeys devuelve las claves del map en orden lexicográfico, para
// que la iteración sea determinística (útil para el "primer no-vacío" y
// para tests).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort — maps típicos tienen 1-5 entries.
	for i := 1; i < len(keys); i++ {
		j := i
		for j > 0 && keys[j-1] > keys[j] {
			keys[j-1], keys[j] = keys[j], keys[j-1]
			j--
		}
	}
	return keys
}
