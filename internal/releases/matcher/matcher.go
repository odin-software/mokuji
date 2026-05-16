// Package matcher decide cuál manga de una source dada (MangaDex, Asura,
// Comick, …) corresponde a un manga de la biblioteca (agregado vía AniList).
//
// Una instancia de Matcher = una source. Library corre N matchers en
// paralelo (uno por source configurada) y persiste el resultado de cada
// uno en una fila de manga_sources.
//
// Estrategia v1:
//
//	1. Buscar en la source con el primer título no-vacío (inglés > romaji >
//	   native, según el orden que pase el caller).
//	2. Normalizar todos los inputs (lower + strip puntuación + collapse
//	   espacios) en un set para comparación O(1).
//	3. Por cada candidato, ver si alguno de sus títulos (title + altTitles
//	   en cualquier idioma) matchea exactamente alguno de los inputs.
//	4. Resolución:
//	     - 1 candidato con match exacto -> matched
//	     - 0 o >1 con match exacto, pero hay candidatos -> needs_review
//	       (devolvemos top N para que el user elija)
//	     - 0 candidatos en absoluto -> no_results
//
// Si el cliente devuelve releasesource.ErrRateLimited, propagamos sin
// envolver para que el caller (worker o handler de "agregar manga")
// reagende.
//
// Sin fuzzy match (Levenshtein, etc.) por ahora — YAGNI. Si la métrica
// de needs_review en producción sube mucho, se agrega.
package matcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"git.odin.do/kengru/mokuji/internal/releasesource"
)

// Status del match final.
type Status string

const (
	StatusUnmatched   Status = "unmatched"
	StatusMatched     Status = "matched"
	StatusNeedsReview Status = "needs_review"
	StatusNoResults   Status = "no_results"
)

// MaxCandidates es el tope de candidatos que devolvemos en needs_review.
// La UI muestra el cover + título y el user elige; mostrar más de 5 cansa.
const MaxCandidates = 5

// Result del intento de matching.
type Result struct {
	Status     Status
	SourceID   string      // poblado solo si Status == StatusMatched; ID/slug específico de la source
	Candidates []Candidate // poblado si Status == StatusNeedsReview
}

// Candidate es la representación delgada de un manga (de la source)
// que se presenta al user para elegir manualmente.
type Candidate struct {
	ID       string
	Title    string
	CoverURL string
	Year     int
}

// SearchClient es lo mínimo que el Matcher necesita del cliente. Permite
// testear con stubs sin levantar httptest.Server. releasesource.Client
// cumple esta firma trivialmente (es un subset).
type SearchClient interface {
	Search(ctx context.Context, title string) ([]releasesource.MangaResult, error)
}

// Matcher empaqueta el cliente + la lógica de scoring.
type Matcher struct {
	client SearchClient
}

// New construye un Matcher con el cliente dado.
func New(client SearchClient) *Matcher {
	return &Matcher{client: client}
}

// Match busca la contraparte de un manga de AniList en MangaDex.
// titles deben ir en orden de preferencia para el query (ej. english,
// romaji, native). El primer no-vacío se usa para la búsqueda; los
// demás se incluyen como inputs adicionales del scoring.
func (m *Matcher) Match(ctx context.Context, titles ...string) (Result, error) {
	inputs := normalizedSet(titles)
	if len(inputs) == 0 {
		return Result{Status: StatusNoResults}, nil
	}

	query := firstNonEmpty(titles)
	candidates, err := m.client.Search(ctx, query)
	if err != nil {
		if errors.Is(err, releasesource.ErrRateLimited) {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("matcher: search: %w", err)
	}

	if len(candidates) == 0 {
		return Result{Status: StatusNoResults}, nil
	}

	var exact []releasesource.MangaResult
	for _, cand := range candidates {
		if hasExactMatch(cand.Titles, inputs) {
			exact = append(exact, cand)
		}
	}

	if len(exact) == 1 {
		return Result{Status: StatusMatched, SourceID: exact[0].ID}, nil
	}

	// 0 o >1 exact matches → pedir review.
	pool := candidates
	if len(exact) > 1 {
		pool = exact
	}
	if len(pool) > MaxCandidates {
		pool = pool[:MaxCandidates]
	}
	out := Result{Status: StatusNeedsReview, Candidates: make([]Candidate, 0, len(pool))}
	for _, c := range pool {
		out.Candidates = append(out.Candidates, Candidate{
			ID:       c.ID,
			Title:    c.Title,
			CoverURL: c.CoverURL,
			Year:     c.Year,
		})
	}
	return out, nil
}

// normalizedSet retorna las versiones normalizadas no-vacías de titles
// como set para lookup O(1).
func normalizedSet(titles []string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, t := range titles {
		if n := normalize(t); n != "" {
			set[n] = struct{}{}
		}
	}
	return set
}

func hasExactMatch(candidateTitles []string, inputs map[string]struct{}) bool {
	for _, t := range candidateTitles {
		if _, ok := inputs[normalize(t)]; ok {
			return true
		}
	}
	return false
}

// normalize transforma s a su forma canónica para comparación:
//   - lowercase Unicode-aware (handle acentos y kana)
//   - letras y dígitos pasan
//   - whitespace y puntuación se tratan como un solo espacio, colapsado
//   - trim de bordes
//
// Ejemplos:
//
//	"Chainsaw Man!"          → "chainsaw man"
//	"  Chainsaw   Man "      → "chainsaw man"
//	"Re:Zero - Starting..."  → "re zero starting"
//	"チェンソーマン"          → "チェンソーマン"
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastWasSpace := true // suprime espacios líderes
	for _, r := range s {
		r = unicode.ToLower(r)
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastWasSpace = false
		default:
			// whitespace y puntuación: colapsamos a un solo espacio
			if !lastWasSpace {
				b.WriteRune(' ')
				lastWasSpace = true
			}
		}
	}
	return strings.TrimRight(b.String(), " ")
}

func firstNonEmpty(ss []string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
