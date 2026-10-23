package library

import "testing"

func TestFormatChapter(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0, ""}, {12, "12"}, {12.5, "12.5"}, {252, "252"},
	} {
		if got := FormatChapter(tc.in); got != tc.want {
			t.Errorf("FormatChapter(%v) = %q, esperado %q", tc.in, got, tc.want)
		}
	}
}
