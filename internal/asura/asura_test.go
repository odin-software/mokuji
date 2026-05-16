package asura

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"git.odin.do/kengru/mokuji/internal/releases/matcher"
	"git.odin.do/kengru/mokuji/internal/releasesource"
)

// noSleep evita esperas reales en retries durante tests.
func noSleep(context.Context, time.Duration) {}

// newTestClient construye un client apuntado al test server, con rate
// limit relajado y sin sleeper real.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(
		WithAPIBaseURL(srv.URL),
		WithFrontendBaseURL("https://asurascans.test"),
		WithRateLimit(time.Microsecond, 100),
		WithSleeper(noSleep),
	)
}

// fixture devuelve los bytes de un archivo en testdata/. Si la lectura
// falla el test aborta inmediatamente.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("leer fixture %s: %v", name, err)
	}
	return b
}

func TestName(t *testing.T) {
	c := New()
	if c.Name() != "asura" {
		t.Errorf("Name(): %q, want \"asura\"", c.Name())
	}
}

func TestSearchParsesFixture(t *testing.T) {
	// Fixture: el resultado real del API para search=the+knight+only+lives+today.
	// Devuelve Eternally Regressing Knight (1 resultado), con AniList-style
	// alt_titles incluyendo "The Knight Only Lives Today".
	body := fixture(t, "search_knight.json")
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/series" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("search"); got != "the knight only lives today" {
			t.Errorf("search param: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})

	results, err := c.Search(context.Background(), "the knight only lives today")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results: esperado 1, obtenido %d", len(results))
	}

	r := results[0]
	if r.ID != "eternally-regressing-knight-030ff47a" {
		t.Errorf("ID: %q, want eternally-regressing-knight-030ff47a", r.ID)
	}
	if r.Title != "Eternally Regressing Knight" {
		t.Errorf("Title: %q", r.Title)
	}
	// El alt_title que matchea con el query AniList debe estar en r.Titles
	// — eso es lo que permite que el matcher exact-match resuelva esto.
	var found bool
	for _, t := range r.Titles {
		if t == "The Knight Only Lives Today" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("alt_title 'The Knight Only Lives Today' ausente de r.Titles: %v", r.Titles)
	}
	if !strings.HasPrefix(r.CoverURL, "https://cdn.asurascans.com/") {
		t.Errorf("CoverURL: %q (no es CDN)", r.CoverURL)
	}
	if r.Status != "ongoing" {
		t.Errorf("Status: %q", r.Status)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no debería haber HTTP request para query vacía; path=%s", r.URL.Path)
	})
	results, err := c.Search(context.Background(), "  ")
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("results: esperado nil, obtenido %v", results)
	}
}

func TestSearchCapsToMaxResults(t *testing.T) {
	// Simulamos respuesta con 20 items para verificar que cortamos a 10.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var b strings.Builder
		b.WriteString(`{"data":[`)
		for i := range 20 {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"slug":"s","title":"T","public_url":"/comics/s"}`)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	})
	results, err := c.Search(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != maxSearchResults {
		t.Errorf("len: esperado %d, obtenido %d", maxSearchResults, len(results))
	}
}

func TestSearchHandlesEmptyPublicURL(t *testing.T) {
	// public_url vacío → fallback al short slug.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"slug":"shortonly","title":"T","public_url":""}]}`))
	})
	results, err := c.Search(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "shortonly" {
		t.Errorf("fallback al short slug falló: %+v", results)
	}
}

func TestFetchChaptersParsesFixture(t *testing.T) {
	body := fixture(t, "chapters_overgeared.json")
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/series/overgeared-030ff47a/chapters") {
			t.Errorf("path: %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})

	chs, err := c.FetchChapters(context.Background(), "overgeared-030ff47a", "en", time.Time{})
	if err != nil {
		t.Fatalf("FetchChapters: %v", err)
	}
	if len(chs) != 5 {
		t.Fatalf("chapters: esperado 5, obtenido %d", len(chs))
	}

	ch := chs[0]
	if ch.Number != "323" {
		t.Errorf("Number: %q", ch.Number)
	}
	if ch.Title != "Another Man's Choice" {
		t.Errorf("Title: %q", ch.Title)
	}
	if ch.Language != "en" {
		t.Errorf("Language: %q", ch.Language)
	}
	wantPub := time.Date(2026, 5, 16, 6, 27, 23, 129_000_000, time.UTC)
	if !ch.PublishedAt.Equal(wantPub) {
		t.Errorf("PublishedAt: %v, want %v", ch.PublishedAt, wantPub)
	}
	wantURL := "https://asurascans.test/comics/overgeared-030ff47a/chapter/323"
	if ch.ExternalURL != wantURL {
		t.Errorf("ExternalURL: %q, want %q", ch.ExternalURL, wantURL)
	}
	// ID debe ser único cross-source — combinar slug + número.
	if ch.ID != "overgeared-030ff47a/323" {
		t.Errorf("ID: %q", ch.ID)
	}
}

func TestFetchChaptersFiltersBySince(t *testing.T) {
	body := fixture(t, "chapters_overgeared.json")
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	// Filter a los publicados después de 2026-05-09T00:00:00Z — debería
	// dejar fuera los más viejos. La fixture tiene 5 caps, los 3 más
	// recientes están después del 2026-05-09.
	since := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	chs, err := c.FetchChapters(context.Background(), "overgeared-030ff47a", "en", since)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range chs {
		if !ch.PublishedAt.After(since) {
			t.Errorf("cap %s con published_at %v no debería estar en la lista (since=%v)", ch.Number, ch.PublishedAt, since)
		}
	}
	if len(chs) == 5 {
		t.Errorf("filtro since=%v no aplicó (todavía hay 5)", since)
	}
}

func TestFetchChaptersSkipsLocked(t *testing.T) {
	// Cap is_locked=true (early access) no debe aparecer — no podemos
	// linkearlo públicamente.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"number":100,"title":"Open","published_at":"2026-05-01T00:00:00Z","is_locked":false},
			{"number":101,"title":"Premium","published_at":"2026-05-02T00:00:00Z","is_locked":true}
		]}`))
	})
	chs, err := c.FetchChapters(context.Background(), "x", "en", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 1 || chs[0].Number != "100" {
		t.Errorf("locked no filtrado: %+v", chs)
	}
}

func TestFetchChaptersHandlesNullData(t *testing.T) {
	// La API devuelve {"data":null} para slugs inexistentes. Nuestro
	// code debe devolver lista vacía sin error.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null}`))
	})
	chs, err := c.FetchChapters(context.Background(), "ghost", "en", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 0 {
		t.Errorf("ghost slug debería dar lista vacía; obtuvo %d caps", len(chs))
	}
}

func TestFetchChaptersRejectsEmptySourceID(t *testing.T) {
	c := New()
	_, err := c.FetchChapters(context.Background(), "", "en", time.Time{})
	if err == nil {
		t.Fatal("esperado error con sourceID vacío")
	}
}

func TestRateLimitedReturnsSentinel(t *testing.T) {
	// 429 sostenido (sin Retry-After largo) → ErrRateLimited después del retry.
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.Search(context.Background(), "x")
	if err != releasesource.ErrRateLimited {
		t.Fatalf("err: %v, want ErrRateLimited", err)
	}
	if calls != 2 {
		t.Errorf("calls: esperado 2 (1 + 1 retry), obtenido %d", calls)
	}
}

func TestRateLimitedHonorsLargeRetryAfter(t *testing.T) {
	// Retry-After > maxRetryAfterWait → abortamos sin retry.
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.Search(context.Background(), "x")
	if err != releasesource.ErrRateLimited {
		t.Fatalf("err: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls: esperado 1 (sin retry), obtenido %d", calls)
	}
}

func TestNotFoundMappedToSentinel(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.FetchChapters(context.Background(), "x", "en", time.Time{})
	if err != releasesource.ErrNotFound {
		t.Errorf("err: %v, want ErrNotFound", err)
	}
}

func TestServerErrorRetriesThenFails(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := c.Search(context.Background(), "x")
	if err == nil {
		t.Fatal("esperado error tras 5xx")
	}
	if calls != 2 {
		t.Errorf("calls: esperado 2 (1 + 1 retry), obtenido %d", calls)
	}
}

// TestMatcherResolvesViaAltTitle es el end-to-end del caso real reportado:
// AniList nos da los títulos "Eternally Regressing Knight" (romaji) y
// "The Knight Only Lives Today" (inglés según Anilist). El matcher de
// internal/releases/matcher, alimentado con el cliente Asura, debería
// devolver StatusMatched apuntando al slug ERK porque alguno de los
// titles que pasamos coincide con alguno de los alt_titles que la API
// devuelve para esa serie.
func TestMatcherResolvesViaAltTitle(t *testing.T) {
	body := fixture(t, "search_knight.json")
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	m := matcher.New(c)

	// Caso 1: AniList nos da el inglés "The Knight Only Lives Today" como
	// primer título. Es un alt_title de ERK en Asura, debería matchear.
	r, err := m.Match(context.Background(), "The Knight Only Lives Today", "Eternally Regressing Knight")
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if r.Status != matcher.StatusMatched {
		t.Fatalf("status: %q (esperaba matched). Result=%+v", r.Status, r)
	}
	if r.SourceID != "eternally-regressing-knight-030ff47a" {
		t.Errorf("SourceID: %q, want eternally-regressing-knight-030ff47a", r.SourceID)
	}

	// Caso 2: solo el título inglés (mismo path pero stress-test del
	// fallback cuando AniList no provee el romaji).
	r, err = m.Match(context.Background(), "The Knight Only Lives Today")
	if err != nil {
		t.Fatalf("Match (single title): %v", err)
	}
	if r.Status != matcher.StatusMatched {
		t.Errorf("status (single): %q", r.Status)
	}
}

func TestSendsUserAgent(t *testing.T) {
	got := make(chan string, 1)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	_, _ = c.Search(context.Background(), "x")
	ua := <-got
	if !strings.Contains(ua, "mokuji") {
		t.Errorf("UA: %q (no contiene 'mokuji')", ua)
	}
}
