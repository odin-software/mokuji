package comick

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

func noSleep(context.Context, time.Duration) {}

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(
		WithAPIBaseURL(srv.URL),
		WithFrontendBaseURL("https://comick.test"),
		WithRateLimit(time.Microsecond, 100),
		WithSleeper(noSleep),
	)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("leer fixture %s: %v", name, err)
	}
	return b
}

func TestName(t *testing.T) {
	if got := New().Name(); got != "comick" {
		t.Errorf("Name(): %q", got)
	}
}

func TestSearchParsesFixture(t *testing.T) {
	body := fixture(t, "search_knight.json")
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("q"); got != "knight" {
			t.Errorf("q: %q", got)
		}
		if r.Header.Get("Referer") == "" {
			t.Error("Referer no enviado — Comick rechaza requests sin él")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})

	results, err := c.Search(context.Background(), "knight")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results: esperado 3, obtenido %d", len(results))
	}
	// El primer item del fixture es "Love Live!..." con varios alt titles
	r0 := results[0]
	if r0.Title == "" || !strings.Contains(r0.Title, "Love Live") {
		t.Errorf("Title[0]: %q", r0.Title)
	}
	if r0.ID == "" {
		t.Errorf("ID[0] vacío")
	}
	// Titles debería incluir el title canónico + los alts sin duplicar
	if len(r0.Titles) < 2 {
		t.Errorf("Titles debería incluir alt_titles, obtenido %d", len(r0.Titles))
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no debería pegarle al API con query vacía; path=%s", r.URL.Path)
	})
	results, _ := c.Search(context.Background(), "  ")
	if results != nil {
		t.Errorf("results: esperado nil, obtenido %v", results)
	}
}

func TestSearchSendsReferer(t *testing.T) {
	got := make(chan string, 1)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Referer")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	_, _ = c.Search(context.Background(), "x")
	if ref := <-got; !strings.Contains(ref, "comick.test") {
		t.Errorf("Referer: %q (esperaba contener el frontend base)", ref)
	}
}

func TestFetchChaptersParsesFixture(t *testing.T) {
	body := fixture(t, "chapters_knight.json")
	called := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.URL.Path != "/api/comics/the-knight-only-lives-today/chapter-list" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("lang"); got != "en" {
			t.Errorf("lang: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})

	chs, err := c.FetchChapters(context.Background(), "the-knight-only-lives-today", "en", time.Time{})
	if err != nil {
		t.Fatalf("FetchChapters: %v", err)
	}
	// La fixture tiene 6 caps + last_page=4. El cliente sigue paginando
	// hasta last_page. En este test el server siempre devuelve la misma
	// página, así que pageHardCap (20) o last_page (4) frena el loop.
	// Le entregamos página 1,2,3,4 todas iguales → 24 chapters dedup-by-ID
	// pero como la fixture tiene 6 hids distintos, el upsert en DB
	// dedupea — acá solo verificamos el primer batch.
	if len(chs) < 6 {
		t.Fatalf("chapters: esperado al menos 6, obtenido %d", len(chs))
	}
	if called != 4 {
		t.Errorf("llamadas al API: %d (esperaba 4 — paginación hasta last_page)", called)
	}

	ch := chs[0]
	if ch.ID != "JW5mkJX" {
		t.Errorf("ID: %q (esperaba el hid del fixture)", ch.ID)
	}
	if ch.Number != "110" {
		t.Errorf("Number: %q", ch.Number)
	}
	// Title del fixture viene "" — Comick deja vacío el título narrativo
	// para muchos caps. El scanlator vive en Group, separado.
	if ch.Title != "" {
		t.Errorf("Title del fixture debería ser vacío, obtenido %q", ch.Title)
	}
	if ch.Group != "Mangakakalot" {
		t.Errorf("Group debería ser 'Mangakakalot' (de group_name del fixture), obtenido %q", ch.Group)
	}
	wantURL := "https://comick.test/comic/the-knight-only-lives-today/JW5mkJX-chapter-110-en"
	if ch.ExternalURL != wantURL {
		t.Errorf("ExternalURL: %q", ch.ExternalURL)
	}
	// PublishedAt sale de created_at del fixture (2026-05-16T13:59:46Z).
	wantPub := time.Date(2026, 5, 16, 13, 59, 46, 0, time.UTC)
	if !ch.PublishedAt.Equal(wantPub) {
		t.Errorf("PublishedAt: %v, want %v", ch.PublishedAt, wantPub)
	}
}

func TestFetchChaptersStopsOnLastPage(t *testing.T) {
	// Cuando current_page >= last_page, el cliente deja de paginar.
	called := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		called++
		w.Header().Set("Content-Type", "application/json")
		// last_page=1 → una sola página.
		_, _ = w.Write([]byte(`{"data":[{"hid":"H1","chap":"1","lang":"en","created_at":"2026-05-01T00:00:00Z"}],"pagination":{"current_page":1,"last_page":1,"total":1}}`))
	})
	if _, err := c.FetchChapters(context.Background(), "x", "en", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Errorf("llamadas: %d (esperaba 1)", called)
	}
}

func TestFetchChaptersFiltersBySince(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"hid":"H1","chap":"1","lang":"en","created_at":"2026-01-01T00:00:00Z"},
			{"hid":"H2","chap":"2","lang":"en","created_at":"2026-03-01T00:00:00Z"},
			{"hid":"H3","chap":"3","lang":"en","created_at":"2026-05-01T00:00:00Z"}
		],"pagination":{"current_page":1,"last_page":1}}`))
	})
	since := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	chs, err := c.FetchChapters(context.Background(), "x", "en", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 2 {
		t.Errorf("filter: esperado 2 caps después de %v, obtenido %d", since, len(chs))
	}
}

func TestFetchChaptersRejectsEmptySlug(t *testing.T) {
	c := New()
	if _, err := c.FetchChapters(context.Background(), "", "en", time.Time{}); err == nil {
		t.Error("esperaba error con slug vacío")
	}
}

func TestForbiddenMappedToRateLimited(t *testing.T) {
	// 403 (síntoma de CF rotando a un dominio gateado) se mapea a
	// ErrRateLimited para que el worker reagende sin tirar el job.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := c.Search(context.Background(), "x")
	if err != releasesource.ErrRateLimited {
		t.Errorf("err: %v, want ErrRateLimited", err)
	}
}

func TestRateLimitedReturnsSentinel(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.Search(context.Background(), "x")
	if err != releasesource.ErrRateLimited {
		t.Fatalf("err: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls: %d (esperaba 1 + 1 retry)", calls)
	}
}

func TestNotFoundMappedToSentinel(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.FetchChapters(context.Background(), "x", "en", time.Time{})
	if err != releasesource.ErrNotFound {
		t.Errorf("err: %v", err)
	}
}

// E2E: el matcher real, alimentado por el cliente Comick, resuelve el
// título de AniList. Igual al test que tenemos para Asura — verifica el
// flow completo "AniList title → Match() → slug Comick".
func TestMatcherResolvesViaComickClient(t *testing.T) {
	// Fixture: la búsqueda real devuelve "The Knight Only Lives Today"
	// como uno de los items. Si el matcher hace exact-match contra ese
	// title, debería devolver Matched con el slug correspondiente.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"slug":"different-manga","title":"Different Manga","titles":["Different Manga"]},
			{"slug":"the-knight-only-lives-today","title":"The Knight Only Lives Today","titles":["The Knight Only Lives Today"]}
		]}`))
	})
	m := matcher.New(c)

	r, err := m.Match(context.Background(), "The Knight Only Lives Today")
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if r.Status != matcher.StatusMatched {
		t.Fatalf("status: %q (esperaba matched). Result=%+v", r.Status, r)
	}
	if r.SourceID != "the-knight-only-lives-today" {
		t.Errorf("SourceID: %q", r.SourceID)
	}
}
