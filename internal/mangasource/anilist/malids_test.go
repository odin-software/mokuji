package anilist

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// malIDsServer responde a idMal_in con un media por id (AniList id =
// 30000 + MAL id), salvo los ids en missing. Registra los lotes recibidos.
// failOnBatch > 0 hace que ese lote (1-indexed) responda 429.
func malIDsServer(t *testing.T, missing map[int]bool, failOnBatch int, batches *[][]int) *Client {
	t.Helper()
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string `json:"query"`
			Variables struct {
				IDs []int `json:"ids"`
			} `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("parse body: %v", err)
		}
		if !strings.Contains(req.Query, "idMal_in") {
			t.Errorf("query sin idMal_in: %q", req.Query)
		}
		*batches = append(*batches, req.Variables.IDs)
		if failOnBatch > 0 && len(*batches) == failOnBatch {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var media []string
		for _, id := range req.Variables.IDs {
			if missing[id] {
				continue
			}
			media = append(media, fmt.Sprintf(
				`{"id": %d, "idMal": %d, "title": {"romaji": "R%d", "english": "", "native": ""}, "coverImage": {"large": "c%d"}}`,
				30000+id, id, id, id))
		}
		fmt.Fprintf(w, `{"data": {"Page": {"media": [%s]}}}`, strings.Join(media, ","))
	})
	return New(WithEndpoint(srv.URL))
}

func seqIDs(n int) []int {
	ids := make([]int, n)
	for i := range ids {
		ids[i] = i + 1
	}
	return ids
}

func TestByMALIDsBatches(t *testing.T) {
	var batches [][]int
	c := malIDsServer(t, nil, 0, &batches)

	got, err := c.ByMALIDs(context.Background(), seqIDs(60))
	if err != nil {
		t.Fatalf("ByMALIDs: %v", err)
	}
	if len(batches) != 2 || len(batches[0]) != 50 || len(batches[1]) != 10 {
		t.Fatalf("lotes: esperado [50 10], obtenido %d lotes", len(batches))
	}
	if len(got) != 60 {
		t.Fatalf("resultados: esperado 60, obtenido %d", len(got))
	}
	r := got[7]
	if r.SourceID != "30007" || r.Title != "R7" || r.CoverURL != "c7" {
		t.Errorf("MAL 7: %+v", r)
	}
}

func TestByMALIDsMissingOmitted(t *testing.T) {
	var batches [][]int
	c := malIDsServer(t, map[int]bool{2: true}, 0, &batches)

	got, err := c.ByMALIDs(context.Background(), []int{1, 2, 3})
	if err != nil {
		t.Fatalf("ByMALIDs: %v", err)
	}
	if _, ok := got[2]; ok {
		t.Error("MAL 2 no tiene mapeo y no debería estar en el map")
	}
	if len(got) != 2 {
		t.Errorf("resultados: esperado 2, obtenido %d", len(got))
	}
}

func TestByMALIDsErrorAborts(t *testing.T) {
	var batches [][]int
	c := malIDsServer(t, nil, 2, &batches)

	if _, err := c.ByMALIDs(context.Background(), seqIDs(60)); err == nil {
		t.Fatal("esperado error cuando el segundo lote responde 429")
	}
}
