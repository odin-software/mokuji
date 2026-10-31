// Package asura implementa un releasesource.Client contra la API JSON de
// Asura Scans (api.asurascans.com).
//
// Asura no tiene API pública documentada — el frontend en asurascans.com
// (build Astro) llama a api.asurascans.com directamente; los endpoints
// que usamos son los que el bundle JS de BrowseFilters expone:
//
//	GET /api/series?search=<q>           → búsqueda (full-text en titles y alt_titles)
//	GET /api/series/<slug>/chapters      → lista de capítulos
//
// Nota: el param es ?search=, no ?name=. ?name= la API lo ignora y
// devuelve los 20 más populares — exactamente la trampa en que caímos
// durante la exploración. ?search= filtra cliente-side por title y
// alt_titles, devolviendo solo coincidencias reales.
//
// Que la API JSON exista (y no estar atrás de Cloudflare managed challenge
// como Comick) hace que podamos hablar con ella desde Go sin browser
// headless. El dominio puede cambiar; lo configuramos vía env var
// ASURA_API_BASE_URL para poder rotar sin redeploy de código.
//
// source_id que persistimos en manga_sources = el "public slug" con sufijo
// hash (ej. "overgeared-030ff47a"), tomado del campo public_url del search
// response. La API de chapters acepta ese slug tal cual, y nos permite
// reconstruir la URL del lector trivialmente:
//
//	https://<frontend>/comics/<source_id>/chapter/<number>
//
// Rate limit conservador (0.5 req/s sostenido, burst 1) — Asura no
// publica su threshold y no queremos que nos baneen.
package asura

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

	"github.com/odin-software/mokuji/internal/releasesource"
)

const (
	// Defaults: el API vive en api.asurascans.com, el frontend (reader)
	// en asurascans.com. Si cambian de dominio, se overridean por env vars.
	defaultAPIBaseURL      = "https://api.asurascans.com"
	defaultFrontendBaseURL = "https://asurascans.com"
	defaultTimeout         = 20 * time.Second // Asura responde lento (~10s típico)
	defaultRateInterval    = 2 * time.Second  // 0.5 req/s sostenido
	defaultRateBurst       = 1
	defaultUserAgent       = "mokuji/0.x (+https://mokuji.org)"

	maxSearchResults  = 10
	maxRetryAfterWait = 10 * time.Second
	defaultRetryDelay = 2 * time.Second

	// SourceName es el identificador que el resto de mokuji usa para
	// referirse a este source — en manga_sources.source, en el map del
	// worker, en logs, etc.
	SourceName = "asura"
)

// Client habla con api.asurascans.com.
type Client struct {
	apiBaseURL      string
	frontendBaseURL string
	http            *http.Client
	rl              *rate.Limiter
	userAgent       string
	sleeper         func(context.Context, time.Duration)
}

// Option configura el Client.
type Option func(*Client)

// WithAPIBaseURL override del endpoint base de la API (útil para tests).
func WithAPIBaseURL(u string) Option { return func(c *Client) { c.apiBaseURL = u } }

// WithFrontendBaseURL override de la base del lector (para construir
// las URLs de chapter.external_url).
func WithFrontendBaseURL(u string) Option { return func(c *Client) { c.frontendBaseURL = u } }

// WithHTTPClient override del *http.Client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithRateLimit override del rate limiter cliente-side.
func WithRateLimit(every time.Duration, burst int) Option {
	return func(c *Client) { c.rl = rate.NewLimiter(rate.Every(every), burst) }
}

// WithUserAgent override del header User-Agent. Asura no pide auth pero
// "Go-http-client/1.1" puede levantar flags en CDNs — siempre identificate.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// WithSleeper override de la función que duerme entre retries. En tests
// se inyecta una versión que no espera tiempo real.
func WithSleeper(f func(context.Context, time.Duration)) Option {
	return func(c *Client) { c.sleeper = f }
}

// New construye un Client con defaults razonables.
func New(opts ...Option) *Client {
	c := &Client{
		apiBaseURL:      defaultAPIBaseURL,
		frontendBaseURL: defaultFrontendBaseURL,
		http:            &http.Client{Timeout: defaultTimeout},
		rl:              rate.NewLimiter(rate.Every(defaultRateInterval), defaultRateBurst),
		userAgent:       defaultUserAgent,
		sleeper:         ctxSleep,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Name cumple releasesource.Client.
func (c *Client) Name() string { return SourceName }

// Search busca series por título. Devuelve hasta maxSearchResults
// candidatos en el orden que devuelve Asura.
func (c *Client) Search(ctx context.Context, title string) ([]releasesource.MangaResult, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, nil
	}

	q := url.Values{}
	q.Set("search", title)
	var resp searchResponse
	if err := c.doGET(ctx, "/api/series", q, &resp); err != nil {
		return nil, err
	}

	limit := min(len(resp.Data), maxSearchResults)
	out := make([]releasesource.MangaResult, 0, limit)
	for _, s := range resp.Data[:limit] {
		out = append(out, s.toResult())
	}
	return out, nil
}

// FetchChapters trae todos los capítulos de una serie. El argumento
// sourceID es el slug del public_url (ej "overgeared-030ff47a"); la API
// también acepta el short slug pero usamos el público para reconstruir
// la reader URL más adelante.
//
// language siempre se reporta como "en" — Asura traduce todo al inglés
// y no expone otros idiomas en el JSON. since filtra a published_at >
// since (cliente-side; el API no acepta el param).
func (c *Client) FetchChapters(ctx context.Context, sourceID, language string, since time.Time) ([]releasesource.Chapter, error) {
	if sourceID == "" {
		return nil, errors.New("asura: sourceID requerido")
	}
	if language == "" {
		language = "en"
	}

	var resp chaptersResponse
	if err := c.doGET(ctx, "/api/series/"+sourceID+"/chapters", nil, &resp); err != nil {
		return nil, err
	}

	out := make([]releasesource.Chapter, 0, len(resp.Data))
	for _, ch := range resp.Data {
		converted, ok := ch.toChapter(c.frontendBaseURL, sourceID, language)
		if !ok {
			continue
		}
		if !since.IsZero() && !converted.PublishedAt.After(since) {
			continue
		}
		out = append(out, converted)
	}
	return out, nil
}

// doGET ejecuta un GET contra el API con rate limiting preventivo y
// manejo de 429/5xx con un retry. Mismo shape que internal/mangadex —
// si en el futuro extraemos esto, va a salir como helper compartido.
func (c *Client) doGET(ctx context.Context, path string, q url.Values, out any) error {
	fullURL := c.apiBaseURL + path
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
			if delay > maxRetryAfterWait {
				return releasesource.ErrRateLimited
			}
			if delay <= 0 {
				delay = defaultRetryDelay
			}
			if attempt == 0 {
				c.sleeper(ctx, delay)
				lastErr = releasesource.ErrRateLimited
				continue
			}
			return releasesource.ErrRateLimited

		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return releasesource.ErrNotFound

		case resp.StatusCode >= 500 && resp.StatusCode < 600:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
			resp.Body.Close()
			lastErr = fmt.Errorf("asura status %d: %s", resp.StatusCode, truncate(string(body), 200))
			if attempt == 0 {
				c.sleeper(ctx, defaultRetryDelay+jitter())
				continue
			}
			return lastErr

		default:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
			resp.Body.Close()
			return fmt.Errorf("asura status %d: %s", resp.StatusCode, truncate(string(body), 200))
		}
	}

	if lastErr != nil {
		return lastErr
	}
	return errors.New("asura: retries agotados")
}

// parseRetryAfter es idéntico al de mangadex — soporta segundos y
// HTTP-date. Duplicado por ahora, candidate a httpcommon en el futuro.
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

func jitter() time.Duration {
	return time.Duration(rand.Int64N(int64(time.Second)))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- Parsing ---

type searchResponse struct {
	Data []seriesEntity `json:"data"`
}

type seriesEntity struct {
	Slug       string   `json:"slug"`
	Title      string   `json:"title"`
	AltTitles  []string `json:"alt_titles"`
	Cover      string   `json:"cover"`
	Status     string   `json:"status"`
	PublicURL  string   `json:"public_url"`
	// Otros campos (description, banner, type, author, artist, genres,
	// chapter_count, latest_chapters, ...) los ignoramos por ahora —
	// no los necesita el matcher ni el worker.
}

// toResult convierte el JSON a releasesource.MangaResult. El ID que
// persistimos es el "public slug" — la parte después de "/comics/" en
// public_url, que incluye el sufijo hash que Asura usa internamente.
// Esa cadena sirve tanto para el endpoint de chapters como para
// reconstruir la URL del lector.
func (s seriesEntity) toResult() releasesource.MangaResult {
	id := publicSlug(s.PublicURL)
	if id == "" {
		// Fallback al short slug si public_url está vacío — el endpoint
		// de chapters lo acepta. La reader URL quedará sin hash pero
		// Asura redirige al canonical de todos modos.
		id = s.Slug
	}
	titles := make([]string, 0, 1+len(s.AltTitles))
	if s.Title != "" {
		titles = append(titles, s.Title)
	}
	titles = append(titles, s.AltTitles...)
	return releasesource.MangaResult{
		ID:       id,
		Title:    s.Title,
		Titles:   titles,
		CoverURL: s.Cover,
		Status:   s.Status,
	}
}

// publicSlug extrae el slug desde "/comics/<slug>" — el campo public_url
// del API. Si el prefijo no coincide, devuelve "" para que el caller
// haga fallback.
func publicSlug(publicURL string) string {
	const prefix = "/comics/"
	if !strings.HasPrefix(publicURL, prefix) {
		return ""
	}
	return strings.TrimSuffix(publicURL[len(prefix):], "/")
}

type chaptersResponse struct {
	Data []chapterEntity `json:"data"`
}

type chapterEntity struct {
	Number      json.Number `json:"number"`
	Title       string      `json:"title"`
	PublishedAt string      `json:"published_at"`
	IsLocked    bool        `json:"is_locked"`
}

// toChapter convierte el JSON a releasesource.Chapter. Capítulos
// is_locked=true (early access detrás del paywall de Asura Premium) los
// ignoramos: no podemos linkearlos públicamente. La ID que persistimos
// es "<seriesSlug>-ch<number>" — único cross-source porque incluye
// la fuente implícita vía el seriesSlug. publishedAt zero (raro pero
// puede pasar en chapters early-access) → skip.
func (e chapterEntity) toChapter(frontendBase, seriesSlug, language string) (releasesource.Chapter, bool) {
	if e.IsLocked {
		return releasesource.Chapter{}, false
	}
	if e.PublishedAt == "" {
		return releasesource.Chapter{}, false
	}
	t, err := time.Parse(time.RFC3339, e.PublishedAt)
	if err != nil {
		return releasesource.Chapter{}, false
	}
	num := strings.TrimSpace(string(e.Number))
	if num == "" {
		return releasesource.Chapter{}, false
	}
	return releasesource.Chapter{
		// El UNIQUE de la tabla chapter es (source, source_id). El
		// source ya identifica a Asura, así que el source_id solo
		// necesita ser único dentro de Asura — combinamos seriesSlug
		// + número para que sea estable y reproducible.
		ID:          seriesSlug + "/" + num,
		Number:      num,
		Title:       e.Title,
		Language:    language,
		PublishedAt: t,
		ExternalURL: frontendBase + "/comics/" + seriesSlug + "/chapter/" + num,
	}, true
}
