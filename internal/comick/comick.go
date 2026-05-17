// Package comick implementa un releasesource.Client contra la API JSON
// de Comick.app — específicamente el subdominio "comick.art" que no
// está atrás del managed challenge de Cloudflare (sí lo está
// comick.dev/comick.live, los dominios "principales" del frontend web).
//
// Cómo lo descubrimos: la extensión de Tachiyomi/Mihon para Comick es
// open source y apuntaba a comick.art como base. Al testearlo desde Go,
// devuelve JSON directo sin challenge intermedio. La extensión usa
// Referer: $baseUrl/ como único header además del User-Agent.
//
// Endpoints:
//
//	GET /api/search?q=<q>                              → búsqueda
//	GET /api/comics/<slug>/chapter-list?lang=<lang>    → capítulos (paginado)
//
// URL del lector que construimos client-side:
//
//	https://comick.art/comic/<slug>/<hid>-chapter-<chap>-<lang>
//
// source_id en manga_sources = el slug (ej. "the-knight-only-lives-today").
// chapter.source_id en la tabla chapter = el hid del capítulo, que es
// único cross-source dentro de Comick.
//
// Cobertura: Comick agrega N scanlation groups por capítulo, por lo que
// pueden aparecer múltiples filas para el mismo número (ch70 subido por
// dos grupos = dos hids distintos). Eso es fiel al UX de Comick. El
// rendering en mokuji agrupa por (manga, número, idioma) y muestra las
// opciones; el dedup vive en la UI, no acá.
package comick

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
	defaultAPIBaseURL      = "https://comick.art"
	defaultFrontendBaseURL = "https://comick.art"
	defaultTimeout         = 20 * time.Second
	defaultRateInterval    = 2 * time.Second // 1 req cada 2s, mismo que la extensión de Tachiyomi
	defaultRateBurst       = 1
	defaultUserAgent       = "mokuji/0.x (+https://mokuji.org)"

	maxSearchResults  = 10
	maxRetryAfterWait = 10 * time.Second
	defaultRetryDelay = 2 * time.Second

	// pageHardCap defiende contra loops si la API se rompe y siempre
	// devuelve la misma página. La serie más grande conocida tiene ~10
	// páginas; 20 da margen.
	pageHardCap = 20

	// SourceName es el identificador del cliente — persistido en
	// manga_sources.source y usado como key en el map del worker.
	SourceName = "comick"
)

// Client habla con comick.art.
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

// WithAPIBaseURL override del endpoint base de la API (tests + env override
// si rotan el dominio JSON-friendly).
func WithAPIBaseURL(u string) Option { return func(c *Client) { c.apiBaseURL = u } }

// WithFrontendBaseURL override de la base del lector — separable de la
// API por si en el futuro divergen.
func WithFrontendBaseURL(u string) Option { return func(c *Client) { c.frontendBaseURL = u } }

// WithHTTPClient override del *http.Client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithRateLimit override del rate limiter cliente-side.
func WithRateLimit(every time.Duration, burst int) Option {
	return func(c *Client) { c.rl = rate.NewLimiter(rate.Every(every), burst) }
}

// WithUserAgent override del header User-Agent.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// WithSleeper override de la función que duerme entre retries (para tests).
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
// candidatos en el orden que devuelve Comick (que es popularidad +
// relevancia mezcladas).
func (c *Client) Search(ctx context.Context, title string) ([]releasesource.MangaResult, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, nil
	}

	q := url.Values{}
	q.Set("q", title)
	var resp searchResponse
	if err := c.doGET(ctx, "/api/search", q, &resp); err != nil {
		return nil, err
	}

	limit := min(len(resp.Data), maxSearchResults)
	out := make([]releasesource.MangaResult, 0, limit)
	for _, s := range resp.Data[:limit] {
		out = append(out, s.toResult())
	}
	return out, nil
}

// FetchChapters trae todos los capítulos de una serie en el idioma dado.
// Sigue paginación hasta last_page (con un hard cap defensivo).
//
// since filtra cliente-side a chapters publicados después de esa fecha.
// Comick usa created_at como "cuándo apareció el cap en la plataforma"
// — publish_at siempre viene null en la respuesta. Esto es buena señal
// para nosotros: el "último cap nuevo" se mide en momento de upload.
func (c *Client) FetchChapters(ctx context.Context, slug, language string, since time.Time) ([]releasesource.Chapter, error) {
	if slug == "" {
		return nil, errors.New("comick: slug requerido")
	}
	if language == "" {
		language = "en"
	}

	var all []releasesource.Chapter
	for page := 1; page <= pageHardCap; page++ {
		q := url.Values{}
		q.Set("lang", language)
		q.Set("page", strconv.Itoa(page))
		var resp chaptersResponse
		if err := c.doGET(ctx, "/api/comics/"+slug+"/chapter-list", q, &resp); err != nil {
			return nil, err
		}
		for _, ch := range resp.Data {
			converted, ok := ch.toChapter(c.frontendBaseURL, slug, language)
			if !ok {
				continue
			}
			if !since.IsZero() && !converted.PublishedAt.After(since) {
				continue
			}
			all = append(all, converted)
		}
		// Salir si llegamos a la última página o vino vacía.
		if len(resp.Data) == 0 || page >= resp.Pagination.LastPage {
			break
		}
	}
	return all, nil
}

// doGET ejecuta un GET contra el API con rate limiting + retry. Mismo
// shape que internal/mangadex y internal/asura (3er caller del patrón).
// Comick requiere el header Referer; sin él la API responde 403.
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
		req.Header.Set("Referer", c.frontendBaseURL+"/")

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

		case resp.StatusCode == http.StatusForbidden:
			// 403 contra comick.art puede ser CF si rotaron el dominio
			// (síntoma: empieza a llegar HTML "Just a moment..." en
			// lugar de JSON). Lo tratamos como rate-limit transitorio
			// para que el worker reagende sin terminar el job entero.
			resp.Body.Close()
			return releasesource.ErrRateLimited

		case resp.StatusCode >= 500 && resp.StatusCode < 600:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
			resp.Body.Close()
			lastErr = fmt.Errorf("comick status %d: %s", resp.StatusCode, truncate(string(body), 200))
			if attempt == 0 {
				c.sleeper(ctx, defaultRetryDelay+jitter())
				continue
			}
			return lastErr

		default:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
			resp.Body.Close()
			return fmt.Errorf("comick status %d: %s", resp.StatusCode, truncate(string(body), 200))
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("comick: retries agotados")
}

// parseRetryAfter es idéntico al de mangadex/asura. TODO: extraer a
// httpcommon cuando haya un cuarto caller — todavía YAGNI.
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
	Slug   string   `json:"slug"`
	Title  string   `json:"title"`
	Titles []string `json:"titles"`
	// El cover URL no viene como string plano en search; Comick usa
	// `md_covers` o `default_thumbnail` según el contexto. Para el MVP
	// del matcher no necesitamos cover (el matcher hace su decision
	// por title/Titles). Cubrir cover requeriría una llamada extra.
	Country       string  `json:"country"`
	Status        int     `json:"status"`         // 1=ongoing, 2=completed (heurística, no documentada)
	LastChapter   float64 `json:"last_chapter"`   // número del último cap; tie-breaker del matcher
	LastChapterAt string  `json:"last_chapter_at"` // RFC3339; tie-breaker secundario
}

// toResult convierte el JSON a releasesource.MangaResult. Titles incluye
// el title canónico (si no está duplicado) + el array titles que Comick
// devuelve con variantes idiomáticas. LastChapter / LastChapterAt sirven
// para que el matcher elija el slug correcto cuando Comick fragmenta una
// serie en N entries (uno por scanlator).
func (s seriesEntity) toResult() releasesource.MangaResult {
	titles := make([]string, 0, len(s.Titles)+1)
	titles = append(titles, s.Title)
	for _, t := range s.Titles {
		if t != "" && t != s.Title {
			titles = append(titles, t)
		}
	}
	var lastAt time.Time
	if s.LastChapterAt != "" {
		if t, err := parseTimestamp(s.LastChapterAt); err == nil {
			lastAt = t
		}
	}
	return releasesource.MangaResult{
		ID:            s.Slug,
		Title:         s.Title,
		Titles:        titles,
		Status:        statusLabel(s.Status),
		LastChapter:   s.LastChapter,
		LastChapterAt: lastAt,
	}
}

// statusLabel mapea el código numérico de status a un string consistente
// con MangaDex/Asura. 1=ongoing es lo más común; los demás son menos
// frecuentes y no están documentados. Default a "" para no inventar.
func statusLabel(code int) string {
	switch code {
	case 1:
		return "ongoing"
	case 2:
		return "completed"
	case 3:
		return "cancelled"
	case 4:
		return "hiatus"
	default:
		return ""
	}
}

type chaptersResponse struct {
	Data       []chapterEntity `json:"data"`
	Pagination paginationInfo  `json:"pagination"`
}

type paginationInfo struct {
	CurrentPage int `json:"current_page"`
	LastPage    int `json:"last_page"`
	Total       int `json:"total"`
}

type chapterEntity struct {
	HID       string `json:"hid"`
	Chap      string `json:"chap"`
	Title     string `json:"title"`
	Lang      string `json:"lang"`
	CreatedAt string `json:"created_at"`
	// group_name es un array; si la API devuelve solo un grupo es []string{"X"}.
	// Lo usamos como ChapterTitle si el campo title viene vacío para que el user
	// vea de qué scanlator es esa fila.
	GroupName []string `json:"group_name"`
}

// toChapter convierte el JSON a releasesource.Chapter. PublishedAt sale
// de created_at (Comick deja publish_at en null casi siempre).
//
// ID del chapter: el hid. Único en Comick.
// ExternalURL: reconstruido con el patrón del lector.
// Group: nombre del scanlator (first de group_name). Separado del Title
// — el chapter title es la "Zaza" / "God and Devil" de la historia, el
// group es "Mangakakalot" / "Zaza Scans". El UI los renderea distinto.
func (e chapterEntity) toChapter(frontendBase, slug, language string) (releasesource.Chapter, bool) {
	if e.HID == "" || e.CreatedAt == "" {
		return releasesource.Chapter{}, false
	}
	t, err := parseTimestamp(e.CreatedAt)
	if err != nil {
		return releasesource.Chapter{}, false
	}
	num := strings.TrimSpace(e.Chap)
	if num == "" {
		return releasesource.Chapter{}, false
	}
	var group string
	if len(e.GroupName) > 0 {
		group = e.GroupName[0]
	}
	return releasesource.Chapter{
		ID:          e.HID,
		Number:      num,
		Title:       e.Title,
		Language:    language,
		PublishedAt: t,
		ExternalURL: frontendBase + "/comic/" + slug + "/" + e.HID + "-chapter-" + num + "-" + language,
		Group:       group,
	}, true
}

// parseTimestamp acepta los dos formatos que Comick mezcla: RFC3339 con
// nanosegundos ("2026-05-16T13:59:46.000000Z") y RFC3339 plano. Probamos
// RFC3339Nano primero — incluye ambos casos.
func parseTimestamp(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
