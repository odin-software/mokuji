package matcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.odin.do/kengru/mokuji/internal/mangadex"
	"git.odin.do/kengru/mokuji/internal/releasesource"
)

// stubClient implementa SearchClient con resultados/error programables y
// captura los queries para verificar comportamiento.
type stubClient struct {
	results []releasesource.MangaResult
	err     error
	queries []string
}

func (s *stubClient) Search(_ context.Context, q string) ([]releasesource.MangaResult, error) {
	s.queries = append(s.queries, q)
	return s.results, s.err
}

func TestMatchSingleExactMatch(t *testing.T) {
	stub := &stubClient{results: []releasesource.MangaResult{
		{ID: "md-1", Title: "Chainsaw Man", Titles: []string{"Chainsaw Man", "チェンソーマン", "Hombre Motosierra"}},
		{ID: "md-2", Title: "Chainsaw Girl", Titles: []string{"Chainsaw Girl"}},
	}}
	m := New(stub)

	r, err := m.Match(context.Background(), "Chainsaw Man", "Chainsaw Man", "チェンソーマン")
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if r.Status != StatusMatched {
		t.Fatalf("status: esperado matched, obtenido %q", r.Status)
	}
	if r.SourceID != "md-1" {
		t.Errorf("SourceID: esperado md-1, obtenido %q", r.SourceID)
	}
	if len(stub.queries) != 1 || stub.queries[0] != "Chainsaw Man" {
		t.Errorf("query: esperado un solo Search('Chainsaw Man'); obtenido %v", stub.queries)
	}
}

func TestMatchMultipleExactMatchesNeedsReview(t *testing.T) {
	// Caso: dos mangas en MangaDex con el mismo título exacto (spin-off, etc.).
	stub := &stubClient{results: []releasesource.MangaResult{
		{ID: "md-1", Title: "Berserk", Titles: []string{"Berserk"}, Year: 1989},
		{ID: "md-2", Title: "Berserk", Titles: []string{"Berserk", "Berserk (spinoff)"}, Year: 2020},
	}}
	m := New(stub)

	r, err := m.Match(context.Background(), "Berserk")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusNeedsReview {
		t.Fatalf("status: esperado needs_review, obtenido %q", r.Status)
	}
	if len(r.Candidates) != 2 {
		t.Errorf("candidates: esperado 2, obtenido %d", len(r.Candidates))
	}
}

func TestMatchNoExactMatchButResultsNeedsReview(t *testing.T) {
	// MangaDex devuelve candidatos pero ninguno coincide exacto con el input.
	stub := &stubClient{results: []releasesource.MangaResult{
		{ID: "md-1", Title: "Chainsaw Man: Buddy Stories", Titles: []string{"Chainsaw Man: Buddy Stories"}},
		{ID: "md-2", Title: "Chainsaw Maid", Titles: []string{"Chainsaw Maid"}},
	}}
	m := New(stub)

	r, err := m.Match(context.Background(), "Chainsaw Man")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusNeedsReview {
		t.Fatalf("status: esperado needs_review, obtenido %q", r.Status)
	}
	if len(r.Candidates) != 2 {
		t.Errorf("candidates: esperado 2, obtenido %d", len(r.Candidates))
	}
}

func TestMatchNoResults(t *testing.T) {
	stub := &stubClient{results: nil}
	m := New(stub)
	r, err := m.Match(context.Background(), "Nothing Matches This Title")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusNoResults {
		t.Errorf("status: esperado no_results, obtenido %q", r.Status)
	}
	if r.SourceID != "" || len(r.Candidates) != 0 {
		t.Errorf("no_results no debería tener id ni candidates; got %+v", r)
	}
}

func TestMatchEmptyTitlesReturnsNoResults(t *testing.T) {
	stub := &stubClient{}
	m := New(stub)
	r, err := m.Match(context.Background(), "", "  ", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusNoResults {
		t.Errorf("status: esperado no_results, obtenido %q", r.Status)
	}
	if len(stub.queries) != 0 {
		t.Errorf("no debería hacer Search con inputs vacíos; queries=%v", stub.queries)
	}
}

func TestMatchPropagatesRateLimitUnwrapped(t *testing.T) {
	stub := &stubClient{err: mangadex.ErrRateLimited}
	m := New(stub)
	_, err := m.Match(context.Background(), "Chainsaw Man")
	if !errors.Is(err, mangadex.ErrRateLimited) {
		t.Fatalf("esperado ErrRateLimited sin envolver, obtenido %v", err)
	}
}

func TestMatchWrapsOtherErrors(t *testing.T) {
	stub := &stubClient{err: errors.New("kaboom")}
	m := New(stub)
	_, err := m.Match(context.Background(), "x")
	if err == nil {
		t.Fatal("se esperaba error")
	}
	if errors.Is(err, mangadex.ErrRateLimited) {
		t.Errorf("error genérico no debería ser ErrRateLimited")
	}
}

func TestMatchUsesFirstNonEmptyTitleAsQuery(t *testing.T) {
	stub := &stubClient{}
	m := New(stub)
	_, err := m.Match(context.Background(), "", "  ", "Romaji Title", "Native")
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.queries) != 1 || stub.queries[0] != "Romaji Title" {
		t.Errorf("query: esperado el primer no-vacío 'Romaji Title'; obtenido %v", stub.queries)
	}
}

func TestMatchCapsNeedsReviewCandidates(t *testing.T) {
	// 10 resultados, ninguno con match exacto → needs_review con top MaxCandidates.
	results := make([]releasesource.MangaResult, 10)
	for i := range results {
		results[i] = releasesource.MangaResult{ID: "id", Title: "Different Title", Titles: []string{"Different Title"}}
	}
	stub := &stubClient{results: results}
	m := New(stub)
	r, err := m.Match(context.Background(), "Original Query")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusNeedsReview {
		t.Fatalf("status: %q", r.Status)
	}
	if len(r.Candidates) != MaxCandidates {
		t.Errorf("candidates capped: esperado %d, obtenido %d", MaxCandidates, len(r.Candidates))
	}
}

func TestMatchUsesAltTitlesForExactMatching(t *testing.T) {
	// El query no matchea el Title principal, pero sí un altTitle (caso típico:
	// AniList nos da el romaji, MangaDex tiene el inglés como primario y el
	// romaji entre los altTitles).
	stub := &stubClient{results: []releasesource.MangaResult{
		{ID: "md-1", Title: "Solo Leveling", Titles: []string{"Solo Leveling", "I Alone Level Up", "나 혼자만 레벨업"}},
	}}
	m := New(stub)
	r, err := m.Match(context.Background(), "나 혼자만 레벨업") // nativo
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusMatched {
		t.Fatalf("debería matchear vía altTitle; got %q", r.Status)
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"":                              "",
		"   ":                           "",
		"Chainsaw Man":                  "chainsaw man",
		"  Chainsaw   Man  ":            "chainsaw man",
		"CHAINSAW MAN!":                 "chainsaw man",
		"Re:Zero − Starting Life":       "re zero starting life",
		"One Piece, Vol. 1":             "one piece vol 1",
		"Spy × Family":                  "spy family",
		"Vagabond.....":                 "vagabond",
		"チェンソーマン":                       "チェンソーマン",
		"!@#$%^&*()":                    "",
		"don't speak":                   "don t speak",  // apostrofes se vuelven espacio (aceptable trade-off)
	}
	for in, want := range cases {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q):\n  esperado %q\n  obtenido %q", in, want, got)
		}
	}
}

// --- Tie-break heurístico cuando hay múltiples exact matches ---
//
// Comick fragmenta una serie en N entries (una por scanlator/variante);
// el matcher debe elegir el slug "canónico" sin pedir intervención
// manual. Estos tests usan datos reales sampleados de comick.art para
// los 3 casos que validamos con el user.

// helper: armar Results con LastChapter / LastChapterAt poblados.
func mk(id, title string, lastCh float64, lastAtYear int, lastAtMonth time.Month, lastAtDay int) releasesource.MangaResult {
	return releasesource.MangaResult{
		ID:            id,
		Title:         title,
		Titles:        []string{title},
		LastChapter:   lastCh,
		LastChapterAt: time.Date(lastAtYear, lastAtMonth, lastAtDay, 0, 0, 0, 0, time.UTC),
	}
}

func TestTieBreakKnightOnlyLivesToday(t *testing.T) {
	// Caso real: "The Knight Only Lives Today" (Anilist) matchea exacto
	// con DOS slugs distintos de Comick:
	//   - the-knight-only-lives-today (last_chapter=110, 2026-05-16)
	//   - eternally-regressing-knight (last_chapter=109, 2026-05-10)
	// Ambos tienen "The Knight Only Lives Today" en sus titles.
	// Heurística: 110 > 109 → gana knight-only-lives.
	stub := &stubClient{results: []releasesource.MangaResult{
		mk("the-knight-only-lives-today", "The Knight Only Lives Today", 110, 2026, time.May, 16),
		{
			ID:            "eternally-regressing-knight",
			Title:         "Eternally Regressing Knight",
			Titles:        []string{"Eternally Regressing Knight", "The Knight Only Lives Today"},
			LastChapter:   109,
			LastChapterAt: time.Date(2026, time.May, 10, 0, 0, 0, 0, time.UTC),
		},
	}}
	m := New(stub)
	r, err := m.Match(context.Background(), "The Knight Only Lives Today")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusMatched {
		t.Fatalf("status: %q (esperaba matched). Result=%+v", r.Status, r)
	}
	if r.SourceID != "the-knight-only-lives-today" {
		t.Errorf("SourceID: %q (esperaba el de mayor LastChapter)", r.SourceID)
	}
}

func TestTieBreakOnePieceFavorsLiveSlug(t *testing.T) {
	// Caso real One Piece en Comick:
	//   - one-piece (last_chapter=1182, last_chapter_at=2026-05-08; al día)
	//   - 02-one-piece (last_chapter=1160, last_chapter_at=2025-09-13; 8 meses sin update)
	// Ambos con title="One Piece" (case difference → normalize iguala).
	// Heurística: 1182 > 1160 → gana one-piece sin caer en needs_review.
	stub := &stubClient{results: []releasesource.MangaResult{
		mk("one-piece", "One Piece", 1182, 2026, time.May, 8),
		mk("02-one-piece", "ONE PIECE", 1160, 2025, time.September, 13),
	}}
	m := New(stub)
	r, err := m.Match(context.Background(), "One Piece")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusMatched {
		t.Fatalf("status: %q (esperaba matched). Result=%+v", r.Status, r)
	}
	if r.SourceID != "one-piece" {
		t.Errorf("SourceID: %q (esperaba one-piece, el slug vivo)", r.SourceID)
	}
}

func TestTieBreakSwordborneFavorsMostCoverage(t *testing.T) {
	// Caso real "The Bastard of Swordborne": Comick devuelve 3 entries
	// (una por scanlator). Heurística debe elegir la de más caps.
	stub := &stubClient{results: []releasesource.MangaResult{
		mk("the-bastard-of-swordborne", "The Bastard of Swordborne", 94, 2026, time.May, 6),
		mk("the-bastard-of-swordborne-official", "The Bastard of Swordborne (Official)", 51, 2025, time.December, 10),
		mk("the-bastard-of-swordborne-flame-comics", "The Bastard of Swordborne (Flame Comics)", 73, 2025, time.December, 10),
	}}
	m := New(stub)
	r, err := m.Match(context.Background(), "The Bastard of Swordborne")
	if err != nil {
		t.Fatal(err)
	}
	// Solo el primer entry hace exact match con el title del query —
	// los otros dos tienen sufijo "(Official)" / "(Flame Comics)" que
	// el normalize() trata como tokens distintos.
	if r.Status != StatusMatched {
		t.Fatalf("status: %q. Result=%+v", r.Status, r)
	}
	if r.SourceID != "the-bastard-of-swordborne" {
		t.Errorf("SourceID: %q", r.SourceID)
	}
}

func TestTieBreakFallsBackToNeedsReviewWhenNoSignals(t *testing.T) {
	// Sin LastChapter ni LastChapterAt (caso Asura/MangaDex), >1 exact
	// match no debería tomar decisión automática — needs_review.
	stub := &stubClient{results: []releasesource.MangaResult{
		{ID: "a", Title: "Berserk", Titles: []string{"Berserk"}},
		{ID: "b", Title: "Berserk", Titles: []string{"Berserk"}},
	}}
	m := New(stub)
	r, err := m.Match(context.Background(), "Berserk")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusNeedsReview {
		t.Errorf("sin señales para tie-break, esperaba needs_review, obtenido %q", r.Status)
	}
}

func TestTieBreakUsesLastChapterAtAsSecondary(t *testing.T) {
	// Empate en LastChapter → desempata por LastChapterAt desc.
	stub := &stubClient{results: []releasesource.MangaResult{
		mk("muerto", "X", 100, 2024, time.January, 1),
		mk("vivo", "X", 100, 2026, time.May, 1),
	}}
	m := New(stub)
	r, err := m.Match(context.Background(), "X")
	if err != nil {
		t.Fatal(err)
	}
	if r.SourceID != "vivo" {
		t.Errorf("SourceID: %q (esperaba el de LastChapterAt más reciente)", r.SourceID)
	}
}

func TestHasExactMatch(t *testing.T) {
	inputs := normalizedSet([]string{"Chainsaw Man"})
	if !hasExactMatch([]string{"Chainsaw Man", "Other"}, inputs) {
		t.Error("debería matchear 'Chainsaw Man'")
	}
	if !hasExactMatch([]string{"CHAINSAW MAN!"}, inputs) {
		t.Error("debería matchear con normalización (caps, signo)")
	}
	if hasExactMatch([]string{"Chainsaw Maid"}, inputs) {
		t.Error("no debería matchear 'Chainsaw Maid'")
	}
}
