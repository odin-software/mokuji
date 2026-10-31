package library

import (
	"strings"

	"github.com/odin-software/mokuji/internal/releases/matcher"
)

// SourceState es lo que el user ve de una source en el detalle (D5). Junta
// estados internos que para el user significan lo mismo: needs_review y
// no_results son "no lo encontramos", porque no tiene cómo resolver la
// duda del matcher.
type SourceState string

const (
	SourceChapters  SourceState = "chapters"
	SourceSearching SourceState = "searching"
	SourceEmpty     SourceState = "empty"
	SourceFailing   SourceState = "failing"
	SourceNotFound  SourceState = "not_found"
)

// sourceState decide el estado visible de una source. polled = el worker
// ya pasó por ella (next_check_at seteado). Con capítulos en DB nunca es
// "buscando", aunque no tenga next_check_at (filas viejas o backfill).
func sourceState(status matcher.Status, polled bool, chapters, failures int) SourceState {
	switch {
	case status == matcher.StatusNeedsReview || status == matcher.StatusNoResults:
		return SourceNotFound
	case status != matcher.StatusMatched:
		return SourceSearching
	case failures > 0:
		return SourceFailing
	case chapters > 0:
		return SourceChapters
	case !polled:
		return SourceSearching
	}
	return SourceEmpty
}

// sourcesSummary es la línea bajo el título del detalle: de dónde salen
// los capítulos y quién no tiene el manga. Vacía cuando no hay nada útil
// que decir (el empty state ya lo explica).
func sourcesSummary(srcs []EntrySource) string {
	var with, without []string
	searching := false
	for _, s := range srcs {
		switch s.State {
		case SourceChapters:
			with = append(with, s.Source)
		case SourceFailing:
			// Una falla transitoria no borra lo que ya trajo: si tiene
			// capítulos sigue siendo de donde salen.
			if s.Chapters > 0 {
				with = append(with, s.Source)
			}
		case SourceNotFound, SourceEmpty:
			without = append(without, s.Source)
		case SourceSearching:
			searching = true
		}
	}
	if len(with) == 0 {
		if searching {
			return "buscando capítulos…"
		}
		return ""
	}
	out := "capítulos de " + joinY(with)
	if len(without) > 0 {
		verb := " no lo tiene"
		if len(without) > 1 {
			verb = " no lo tienen"
		}
		out += " · " + joinY(without) + verb
	}
	return out
}

// joinY une con comas y "y" antes del último: a / a y b / a, b y c.
func joinY(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " y " + items[len(items)-1]
}
