package mangadex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestClient construye un Client apuntando al httptest.Server con rate
// limiter generoso (no bloquea en tests) y sleeper no-op (no espera real).
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(
		WithEndpoint(srv.URL),
		WithRateLimit(time.Microsecond, 100),
		WithSleeper(func(context.Context, time.Duration) {}),
	)
}

// --- Search ---

func TestSearchSuccess(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manga" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.URL.Query().Get("title") != "chainsaw man" {
			t.Errorf("title query: %q", r.URL.Query().Get("title"))
		}
		ratings := r.URL.Query()["contentRating[]"]
		if len(ratings) != 3 || ratings[0] != "safe" {
			t.Errorf("contentRating filter mal aplicado: %v", ratings)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"result": "ok",
			"data": [
				{
					"id": "a1c7c817-4e59-43b7-9365-09675a149a6f",
					"type": "manga",
					"attributes": {
						"title": {"en": "Chainsaw Man", "ja": "チェンソーマン"},
						"altTitles": [{"ja-ro": "Chenso Man"}, {"es": "Hombre Motosierra"}],
						"year": 2018,
						"status": "ongoing"
					},
					"relationships": [
						{"id": "cov-1", "type": "cover_art", "attributes": {"fileName": "cover.jpg"}},
						{"id": "auth-1", "type": "author"}
					]
				}
			],
			"total": 1
		}`))
	})

	results, err := c.Search(context.Background(), "chainsaw man")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("esperado 1 resultado, obtenido %d", len(results))
	}
	r := results[0]
	if r.ID != "a1c7c817-4e59-43b7-9365-09675a149a6f" {
		t.Errorf("ID: %q", r.ID)
	}
	if r.Title != "Chainsaw Man" {
		t.Errorf("Title preferido inglés esperado, obtenido %q", r.Title)
	}
	if len(r.Titles) != 4 {
		t.Errorf("Titles: esperado 4 (en, ja, ja-ro, es), obtenido %d: %v", len(r.Titles), r.Titles)
	}
	if r.CoverURL != "https://uploads.mangadex.org/covers/a1c7c817-4e59-43b7-9365-09675a149a6f/cover.jpg" {
		t.Errorf("CoverURL: %q", r.CoverURL)
	}
	if r.Year != 2018 {
		t.Errorf("Year: %d", r.Year)
	}
	if r.Status != "ongoing" {
		t.Errorf("Status: %q", r.Status)
	}
}

func TestSearchPrefersEnglishTitle(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"result": "ok",
			"data": [{
				"id": "m1", "type": "manga",
				"attributes": {"title": {"ja": "ジャパニーズ"}, "altTitles": []},
				"relationships": []
			}, {
				"id": "m2", "type": "manga",
				"attributes": {"title": {"en": "English", "ja": "ジャパニーズ"}, "altTitles": []},
				"relationships": []
			}],
			"total": 2
		}`))
	})

	results, err := c.Search(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Title != "ジャパニーズ" {
		t.Errorf("sin inglés, esperado primero por orden de keys; obtenido %q", results[0].Title)
	}
	if results[1].Title != "English" {
		t.Errorf("con inglés disponible, esperado 'English'; obtenido %q", results[1].Title)
	}
}

func TestSearchEmptyQueryReturnsNil(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no debería hacer request con query vacía")
	})
	results, err := c.Search(context.Background(), "   ")
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("esperado nil, obtenido %v", results)
	}
}

func TestSearchHandlesHTTPError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "bad request"}`))
	})
	_, err := c.Search(context.Background(), "q")
	if err == nil {
		t.Fatal("se esperaba error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error no incluye status: %v", err)
	}
}

// --- Rate limiting / retry ---

func TestSearch429RetriesAfterRespectingHeader(t *testing.T) {
	var calls int
	var mu sync.Mutex
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"ok","data":[],"total":0}`))
	})

	_, err := c.Search(context.Background(), "q")
	if err != nil {
		t.Fatalf("esperado éxito después del retry, obtenido %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("esperado 2 llamadas (429 + retry exitoso), obtenido %d", calls)
	}
}

func TestSearch429GivesUpAfterSecondAttempt(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.Search(context.Background(), "q")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("esperado ErrRateLimited después de 2 intentos, obtenido %v", err)
	}
}

func TestSearch429RetryAfterTooLongAborts(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60") // > maxRetryAfterDelay (10s)
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.Search(context.Background(), "q")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("esperado ErrRateLimited inmediato si Retry-After excede el cap; obtenido %v", err)
	}
}

func TestSearch5xxRetries(t *testing.T) {
	var calls int
	var mu sync.Mutex
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`oops`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"ok","data":[],"total":0}`))
	})
	_, err := c.Search(context.Background(), "q")
	if err != nil {
		t.Fatalf("esperado éxito después del retry de 5xx, obtenido %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("esperado 2 calls, obtenido %d", calls)
	}
}

// --- FetchChapters ---

func TestFetchChaptersSuccess(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manga/m1/feed" {
			t.Errorf("path: %q", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q["translatedLanguage[]"]; len(got) != 1 || got[0] != "en" {
			t.Errorf("translatedLanguage: %v", got)
		}
		if q.Get("order[publishAt]") != "asc" {
			t.Errorf("order: %q", q.Get("order[publishAt]"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"result": "ok",
			"data": [
				{
					"id": "ch-1", "type": "chapter",
					"attributes": {
						"chapter": "1", "title": "First", "translatedLanguage": "en",
						"publishAt": "2024-01-15T10:00:00+00:00"
					}
				},
				{
					"id": "ch-2", "type": "chapter",
					"attributes": {
						"chapter": "2.5", "title": "Special", "translatedLanguage": "en",
						"externalUrl": "https://mangaplus.shueisha.co.jp/x",
						"publishAt": "2024-02-01T10:00:00+00:00"
					}
				}
			],
			"total": 2
		}`))
	})

	chapters, err := c.FetchChapters(context.Background(), "m1", "en", time.Time{})
	if err != nil {
		t.Fatalf("FetchChapters: %v", err)
	}
	if len(chapters) != 2 {
		t.Fatalf("esperado 2 caps, obtenido %d", len(chapters))
	}
	if chapters[0].Number != "1" {
		t.Errorf("first chapter number: %q", chapters[0].Number)
	}
	if chapters[1].Number != "2.5" {
		t.Errorf("second chapter number: %q", chapters[1].Number)
	}
	if chapters[0].ExternalURL != "" {
		t.Errorf("primer cap sin externalUrl: ExternalURL debería ser vacío, obtenido %q", chapters[0].ExternalURL)
	}
	if chapters[1].ExternalURL != "https://mangaplus.shueisha.co.jp/x" {
		t.Errorf("segundo cap con externalUrl: parser no captura el campo, obtenido %q", chapters[1].ExternalURL)
	}
	if chapters[0].PublishedAt.IsZero() {
		t.Error("PublishedAt no debería ser zero")
	}
}

func TestFetchChaptersPaginates(t *testing.T) {
	var pages int
	var mu sync.Mutex
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pages++
		page := pages
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch page {
		case 1:
			if r.URL.Query().Get("offset") != "0" {
				t.Errorf("page 1 offset: %q", r.URL.Query().Get("offset"))
			}
			// Devolvemos 100 caps "1" en bulk para simular llena la página.
			var sb strings.Builder
			sb.WriteString(`{"result":"ok","total":150,"data":[`)
			for i := range 100 {
				if i > 0 {
					sb.WriteString(",")
				}
				sb.WriteString(`{"id":"ch","type":"chapter","attributes":{"chapter":"x","translatedLanguage":"en","publishAt":"2024-01-15T10:00:00+00:00"}}`)
			}
			sb.WriteString(`]}`)
			_, _ = w.Write([]byte(sb.String()))
		case 2:
			if r.URL.Query().Get("offset") != "100" {
				t.Errorf("page 2 offset: %q", r.URL.Query().Get("offset"))
			}
			var sb strings.Builder
			sb.WriteString(`{"result":"ok","total":150,"data":[`)
			for i := range 50 {
				if i > 0 {
					sb.WriteString(",")
				}
				sb.WriteString(`{"id":"ch","type":"chapter","attributes":{"chapter":"x","translatedLanguage":"en","publishAt":"2024-02-15T10:00:00+00:00"}}`)
			}
			sb.WriteString(`]}`)
			_, _ = w.Write([]byte(sb.String()))
		default:
			t.Errorf("no se esperaba page %d", page)
		}
	})

	chapters, err := c.FetchChapters(context.Background(), "m1", "en", time.Time{})
	if err != nil {
		t.Fatalf("FetchChapters: %v", err)
	}
	if len(chapters) != 150 {
		t.Fatalf("esperado 150 caps (100+50), obtenido %d", len(chapters))
	}
	mu.Lock()
	defer mu.Unlock()
	if pages != 2 {
		t.Errorf("esperado 2 páginas, obtenido %d", pages)
	}
}

func TestFetchChaptersFiltersBySince(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("publishAtSince")
		if got != "2024-03-01T00:00:00" {
			t.Errorf("publishAtSince: esperado '2024-03-01T00:00:00', obtenido %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"ok","total":0,"data":[]}`))
	})

	since := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	_, err := c.FetchChapters(context.Background(), "m1", "en", since)
	if err != nil {
		t.Fatal(err)
	}
}

func TestFetchChaptersSkipsChaptersWithInvalidPublishAt(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"result": "ok", "total": 3,
			"data": [
				{"id": "ok-1", "type": "chapter", "attributes": {"chapter":"1","translatedLanguage":"en","publishAt":"2024-01-15T10:00:00+00:00"}},
				{"id": "bad", "type": "chapter", "attributes": {"chapter":"2","translatedLanguage":"en","publishAt":""}},
				{"id": "ok-2", "type": "chapter", "attributes": {"chapter":"3","translatedLanguage":"en","publishAt":"not-a-date"}}
			]
		}`))
	})

	chapters, err := c.FetchChapters(context.Background(), "m1", "en", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 1 {
		t.Fatalf("esperado 1 cap (saltando los 2 con publishAt inválido), obtenido %d", len(chapters))
	}
	if chapters[0].ID != "ok-1" {
		t.Errorf("cap conservado debería ser ok-1, obtenido %q", chapters[0].ID)
	}
}

func TestFetchChapters404(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"result":"error"}`))
	})
	_, err := c.FetchChapters(context.Background(), "no-existe", "en", time.Time{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("esperado ErrNotFound, obtenido %v", err)
	}
}

// --- Helpers ---

func TestParseRetryAfterSeconds(t *testing.T) {
	cases := map[string]time.Duration{
		"":        0,
		"  ":      0,
		"5":       5 * time.Second,
		"0":       0,
		"-1":      0,
		"not-num": 0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q): esperado %v, obtenido %v", in, want, got)
		}
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(7 * time.Second).UTC().Format(http.TimeFormat)
	d := parseRetryAfter(future)
	if d < 5*time.Second || d > 8*time.Second {
		t.Errorf("parseRetryAfter(future): esperado ~7s, obtenido %v", d)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("parseRetryAfter(past): esperado 0, obtenido %v", got)
	}
}
