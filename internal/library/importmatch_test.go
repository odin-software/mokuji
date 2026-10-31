package library

import (
	"context"
	"testing"

	"github.com/odin-software/mokuji/internal/releases/matcher"
)

func TestMatchImportedPollsInlineInOrder(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()
	withMatcher(h, &fakeMatcher{result: matcher.Result{Status: matcher.StatusMatched, SourceID: "mdx-1"}})
	poller := newFakePoller()
	h.poller = poller

	a, _ := h.upsertManga(ctx, "anilist", "1", "Berserk", "")
	b, _ := h.upsertManga(ctx, "anilist", "2", "Vagabond", "")
	h.matchImported(ctx, []ImportedManga{{MangaID: a, Title: "Berserk"}, {MangaID: b, Title: "Vagabond"}})

	// Sin waitForCall: al volver matchImported los polls ya tienen que estar
	// hechos (en línea, no fire-and-forget).
	calls := poller.callsSnapshot()
	if len(calls) != 2 || calls[0].mangaID != a || calls[1].mangaID != b {
		t.Fatalf("PollOne en orden [a b]: obtenido %+v", calls)
	}
	if status, _ := sourceStatusForManga(t, db, b); status != "matched" {
		t.Errorf("manga_sources de b: %q", status)
	}
}

func TestMatchImportedStopsOnCancel(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	m := &fakeMatcher{result: matcher.Result{Status: matcher.StatusMatched, SourceID: "mdx-1"}}
	withMatcher(h, m)
	h.poller = newFakePoller()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.matchImported(ctx, []ImportedManga{{MangaID: 1, Title: "Berserk"}})

	if len(m.calls) != 0 {
		t.Errorf("con ctx cancelado no debería matchear; calls: %v", m.calls)
	}
}
