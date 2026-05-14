package anilist

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestSearchSuccess(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("método: esperado POST, obtenido %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type: esperado application/json, obtenido %q", ct)
		}

		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("parse request body: %v", err)
		}
		if !strings.Contains(req.Query, "media(search:") {
			t.Errorf("query no contiene 'media(search:': %q", req.Query)
		}
		if req.Variables["search"] != "berserk" {
			t.Errorf("variable search: esperado 'berserk', obtenido %v", req.Variables["search"])
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"Page": {
					"media": [
						{
							"id": 30002,
							"title": {"romaji": "Berserk", "english": "Berserk", "native": "ベルセルク"},
							"coverImage": {"large": "https://cdn.example.com/berserk.jpg"},
							"description": "<i>Dark fantasy manga</i><br>by Kentaro Miura &amp; team."
						}
					]
				}
			}
		}`))
	})

	c := New(WithEndpoint(srv.URL))
	results, err := c.Search(context.Background(), "berserk")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("resultados: esperado 1, obtenido %d", len(results))
	}

	r := results[0]
	if r.SourceID != "30002" {
		t.Errorf("SourceID: %q", r.SourceID)
	}
	if r.Title != "Berserk" {
		t.Errorf("Title: %q", r.Title)
	}
	if r.CoverURL != "https://cdn.example.com/berserk.jpg" {
		t.Errorf("CoverURL: %q", r.CoverURL)
	}
	if strings.Contains(r.Description, "<i>") || strings.Contains(r.Description, "<br>") {
		t.Errorf("description aún tiene tags HTML: %q", r.Description)
	}
	if !strings.Contains(r.Description, "&") {
		t.Errorf("html.UnescapeString no convirtió &amp;: %q", r.Description)
	}
	if !strings.Contains(r.Description, "\n") {
		t.Errorf("br no se convirtió en newline: %q", r.Description)
	}
}

func TestSearchPrefersEnglishOverRomajiOverNative(t *testing.T) {
	cases := []struct {
		name             string
		english, romaji, native string
		want             string
	}{
		{"all present", "Eng", "Rom", "Nat", "Eng"},
		{"no english", "", "Rom", "Nat", "Rom"},
		{"only native", "", "", "Nat", "Nat"},
		{"all empty", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := preferTitle(tc.english, tc.romaji, tc.native); got != tc.want {
				t.Errorf("preferTitle: esperado %q, obtenido %q", tc.want, got)
			}
		})
	}
}

func TestSearchSkipsEntriesWithoutTitle(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"Page": {
					"media": [
						{"id": 1, "title": {"romaji": "", "english": "", "native": ""}, "coverImage": {"large": ""}, "description": ""},
						{"id": 2, "title": {"romaji": "Real", "english": "", "native": ""}, "coverImage": {"large": ""}, "description": ""}
					]
				}
			}
		}`))
	})

	c := New(WithEndpoint(srv.URL))
	results, err := c.Search(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("esperado 1 resultado (saltando el sin título), obtenido %d", len(results))
	}
	if results[0].SourceID != "2" {
		t.Errorf("SourceID: esperado 2, obtenido %q", results[0].SourceID)
	}
}

func TestSearchHandlesGraphQLErrors(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"errors": [{"message": "Variable missing"}],
			"data": null
		}`))
	})

	c := New(WithEndpoint(srv.URL))
	_, err := c.Search(context.Background(), "q")
	if err == nil {
		t.Fatal("se esperaba error de GraphQL")
	}
	if !strings.Contains(err.Error(), "Variable missing") {
		t.Errorf("error no incluye mensaje original: %v", err)
	}
}

func TestSearchHandlesHTTPError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`server down`))
	})

	c := New(WithEndpoint(srv.URL))
	_, err := c.Search(context.Background(), "q")
	if err == nil {
		t.Fatal("se esperaba error HTTP")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error no incluye status: %v", err)
	}
}

func TestSearchEmptyQueryReturnsNil(t *testing.T) {
	// Sin query no hacemos request a AniList (ahorra una llamada gratuita).
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no debería hacer request con query vacía")
	})

	c := New(WithEndpoint(srv.URL))
	results, err := c.Search(context.Background(), "   ")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if results != nil {
		t.Errorf("esperado nil, obtenido %v", results)
	}
}

func TestCleanDescription(t *testing.T) {
	cases := map[string]string{
		"<i>Italic</i>":                "Italic",
		"line1<br>line2":               "line1\nline2",
		"line1<br/>line2":              "line1\nline2",
		"line1<br />line2":             "line1\nline2",
		"&amp; &lt;test&gt;":           "& <test>",
		"<i>mix</i><br>of <b>tags</b>": "mix\nof tags",
		"   leading and trailing   ":   "leading and trailing",
	}
	for in, want := range cases {
		if got := cleanDescription(in); got != want {
			t.Errorf("cleanDescription(%q):\n  esperado %q\n  obtenido %q", in, want, got)
		}
	}
}

func TestClientName(t *testing.T) {
	if name := New().Name(); name != "anilist" {
		t.Errorf("Name: esperado anilist, obtenido %q", name)
	}
}
