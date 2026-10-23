// Package anilist implementa mangasource.Source contra el GraphQL API
// público de AniList (https://anilist.co).
//
// La API es anónima (sin auth), rate limit generoso (~90 req/min por IP),
// y cubre ~100k entries de manga incluyendo título inglés/romaji/nativo,
// covers, y descripciones.
package anilist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.odin.do/kengru/mokuji/internal/mangasource"
)

const (
	defaultEndpoint = "https://graphql.anilist.co"
	defaultTimeout  = 10 * time.Second
	maxResults      = 20
)

// Client habla con la API GraphQL de AniList.
type Client struct {
	endpoint string
	http     *http.Client
}

// Option configura un Client.
type Option func(*Client)

// WithEndpoint override del endpoint (útil para tests con httptest.Server).
func WithEndpoint(url string) Option {
	return func(c *Client) { c.endpoint = url }
}

// WithHTTPClient override del http.Client (útil para tests o para inyectar
// timeouts/transport custom).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New construye un Client.
func New(opts ...Option) *Client {
	c := &Client{
		endpoint: defaultEndpoint,
		http:     &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Name retorna el identificador de la fuente, usado en manga.source.
func (c *Client) Name() string { return "anilist" }

const searchQuery = `query ($search: String, $perPage: Int) {
  Page(perPage: $perPage) {
    media(search: $search, type: MANGA, sort: SEARCH_MATCH) {
      id
      title { romaji english native }
      coverImage { large }
      description
      format
      countryOfOrigin
      status
      chapters
      startDate { year }
    }
  }
}`

type searchResponse struct {
	Data struct {
		Page struct {
			Media []struct {
				ID    int `json:"id"`
				Title struct {
					Romaji  string `json:"romaji"`
					English string `json:"english"`
					Native  string `json:"native"`
				} `json:"title"`
				CoverImage struct {
					Large string `json:"large"`
				} `json:"coverImage"`
				Description     string `json:"description"`
				Format          string `json:"format"`
				CountryOfOrigin string `json:"countryOfOrigin"`
				Status          string `json:"status"`
				Chapters        int    `json:"chapters"`
				StartDate       struct {
					Year int `json:"year"`
				} `json:"startDate"`
			} `json:"media"`
		} `json:"Page"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Search busca manga en AniList. Retorna hasta maxResults resultados,
// ordenados por relevancia (SEARCH_MATCH de AniList).
func (c *Client) Search(ctx context.Context, query string) ([]mangasource.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	var resp searchResponse
	if err := c.do(ctx, searchQuery, map[string]any{
		"search":  query,
		"perPage": maxResults,
	}, &resp); err != nil {
		return nil, err
	}

	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("anilist: %s", resp.Errors[0].Message)
	}

	out := make([]mangasource.SearchResult, 0, len(resp.Data.Page.Media))
	for _, m := range resp.Data.Page.Media {
		title := preferTitle(m.Title.English, m.Title.Romaji, m.Title.Native)
		if title == "" {
			continue
		}
		var alt string
		if title == m.Title.English && m.Title.Romaji != "" && m.Title.Romaji != title {
			alt = m.Title.Romaji
		}
		out = append(out, mangasource.SearchResult{
			SourceID:    strconv.Itoa(m.ID),
			Title:       title,
			CoverURL:    m.CoverImage.Large,
			Description: cleanDescription(m.Description),
			AltTitle:    alt,
			Format:      m.Format,
			Country:     m.CountryOfOrigin,
			Year:        m.StartDate.Year,
			Chapters:    m.Chapters,
			Status:      m.Status,
		})
	}

	// One-shots y novelas al final, sin ocultarlos (D7): con un título
	// casi igual al del manga, aparecían primero y parecían duplicados.
	// Partición estable para no romper el orden de relevancia de AniList.
	sort.SliceStable(out, func(i, j int) bool {
		return !demoted(out[i].Format) && demoted(out[j].Format)
	})
	return out, nil
}

func (c *Client) do(ctx context.Context, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("crear request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap defensivo
	if err != nil {
		return fmt.Errorf("leer respuesta: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("anilist status %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("parse json: %w", err)
	}

	return nil
}

// preferTitle retorna el primero no-vacío entre los candidatos.
func demoted(format string) bool { return format == "ONE_SHOT" || format == "NOVEL" }

func preferTitle(candidates ...string) string {
	for _, s := range candidates {
		if s != "" {
			return s
		}
	}
	return ""
}

var (
	brTag      = regexp.MustCompile(`(?i)<br\s*/?>`)
	anyHTMLTag = regexp.MustCompile(`<[^>]+>`)
)

// cleanDescription transforma la descripción HTML de AniList a texto plano
// preservando saltos de línea de los <br>.
func cleanDescription(s string) string {
	s = brTag.ReplaceAllString(s, "\n")
	s = anyHTMLTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// malBatchSize es el máximo de ids por request de ByMALIDs: el perPage tope
// de AniList.
const malBatchSize = 50

const byMALIDsQuery = `query ($ids: [Int]) {
  Page(perPage: 50) {
    media(idMal_in: $ids, type: MANGA) {
      id
      idMal
      title { romaji english native }
      coverImage { large }
    }
  }
}`

type byMALIDsResponse struct {
	Data struct {
		Page struct {
			Media []struct {
				ID    int `json:"id"`
				IDMal int `json:"idMal"`
				Title struct {
					Romaji  string `json:"romaji"`
					English string `json:"english"`
					Native  string `json:"native"`
				} `json:"title"`
				CoverImage struct {
					Large string `json:"large"`
				} `json:"coverImage"`
			} `json:"media"`
		} `json:"Page"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// ByMALIDs resuelve ids de MyAnimeList a mangas de AniList usando el campo
// idMal, en lotes de 50. Es un mapeo exacto: los ids sin equivalente no
// aparecen en el map (el import no adivina por título). Cualquier error en
// cualquier lote aborta todo: el import no escribe nada a medias.
func (c *Client) ByMALIDs(ctx context.Context, ids []int) (map[int]mangasource.SearchResult, error) {
	out := make(map[int]mangasource.SearchResult, len(ids))
	for start := 0; start < len(ids); start += malBatchSize {
		batch := ids[start:min(start+malBatchSize, len(ids))]
		var resp byMALIDsResponse
		if err := c.do(ctx, byMALIDsQuery, map[string]any{"ids": batch}, &resp); err != nil {
			return nil, err
		}
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("anilist: %s", resp.Errors[0].Message)
		}
		for _, m := range resp.Data.Page.Media {
			title := preferTitle(m.Title.English, m.Title.Romaji, m.Title.Native)
			if title == "" || m.IDMal == 0 {
				continue
			}
			out[m.IDMal] = mangasource.SearchResult{
				SourceID: strconv.Itoa(m.ID),
				Title:    title,
				CoverURL: m.CoverImage.Large,
			}
		}
	}
	return out, nil
}
