package library

import (
	"testing"

	"git.odin.do/kengru/mokuji/internal/releases/matcher"
)

func TestSourceState(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   matcher.Status
		polled   bool
		chapters int
		failures int
		want     SourceState
	}{
		{"needs_review", matcher.StatusNeedsReview, false, 0, 0, SourceNotFound},
		{"no_results", matcher.StatusNoResults, true, 0, 0, SourceNotFound},
		{"unmatched", matcher.StatusUnmatched, false, 0, 0, SourceSearching},
		{"matched con fallas", matcher.StatusMatched, true, 12, 2, SourceFailing},
		{"matched sin pollear", matcher.StatusMatched, false, 0, 0, SourceSearching},
		{"matched con caps", matcher.StatusMatched, true, 12, 0, SourceChapters},
		{"matched polleado sin caps", matcher.StatusMatched, true, 0, 0, SourceEmpty},
	} {
		if got := sourceState(tc.status, tc.polled, tc.chapters, tc.failures); got != tc.want {
			t.Errorf("%s: %q, esperado %q", tc.name, got, tc.want)
		}
	}
}

func TestSourcesSummary(t *testing.T) {
	src := func(name string, st SourceState) EntrySource { return EntrySource{Source: name, State: st} }
	for _, tc := range []struct {
		in   []EntrySource
		want string
	}{
		{[]EntrySource{src("asura", SourceNotFound), src("comick", SourceNotFound), src("mangadex", SourceChapters)},
			"capítulos de mangadex · asura y comick no lo tienen"},
		{[]EntrySource{src("asura", SourceEmpty), src("mangadex", SourceChapters)},
			"capítulos de mangadex · asura no lo tiene"},
		{[]EntrySource{src("mangadex", SourceChapters)}, "capítulos de mangadex"},
		{[]EntrySource{src("asura", SourceChapters), src("comick", SourceChapters), src("mangadex", SourceChapters)},
			"capítulos de asura, comick y mangadex"},
		{[]EntrySource{src("mangadex", SourceSearching)}, "buscando capítulos…"},
		{[]EntrySource{src("asura", SourceEmpty)}, ""},
	} {
		if got := sourcesSummary(tc.in); got != tc.want {
			t.Errorf("sourcesSummary(%v) = %q, esperado %q", tc.in, got, tc.want)
		}
	}
}
