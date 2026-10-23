package mangasource

import "testing"

func TestMetaLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   SearchResult
		want string
	}{
		{"haikyu", SearchResult{Format: "MANGA", Country: "JP", Year: 2012, Chapters: 404, Status: "FINISHED"}, "manga · 2012 · 404 caps · terminado"},
		{"piloto", SearchResult{Format: "ONE_SHOT", Country: "JP", Year: 2011, Chapters: 1, Status: "FINISHED"}, "one-shot · 2011 · 1 cap · terminado"},
		{"solo leveling", SearchResult{Format: "MANGA", Country: "KR", Year: 2018, Chapters: 201, Status: "FINISHED"}, "manhwa · 2018 · 201 caps · terminado"},
		{"en emisión sin caps", SearchResult{Format: "MANGA", Country: "KR", Year: 2024, Status: "RELEASING"}, "manhwa · 2024 · en emisión"},
		{"manhua", SearchResult{Format: "MANGA", Country: "CN", Status: "HIATUS"}, "manhua · en pausa"},
		{"novela", SearchResult{Format: "NOVEL", Country: "JP", Status: "NOT_YET_RELEASED"}, "novela · próximamente"},
		{"vacío", SearchResult{}, ""},
	} {
		if got := tc.in.MetaLine(); got != tc.want {
			t.Errorf("%s: %q, esperado %q", tc.name, got, tc.want)
		}
	}
}
