package mangasource

import (
	"strconv"
	"strings"
)

// MetaLine es la línea de metadata bajo cada resultado de búsqueda (D7):
// "manga · 2012 · 404 caps · terminado". Responde a F1: el manga y sus
// one-shots piloto tienen casi el mismo título y sin esto no hay cómo
// distinguirlos. Omite lo que no vino.
func (r SearchResult) MetaLine() string {
	var parts []string
	if t := kind(r.Format, r.Country); t != "" {
		parts = append(parts, t)
	}
	if r.Year > 0 {
		parts = append(parts, strconv.Itoa(r.Year))
	}
	switch {
	case r.Chapters == 1:
		parts = append(parts, "1 cap")
	case r.Chapters > 1:
		parts = append(parts, strconv.Itoa(r.Chapters)+" caps")
	}
	if s := statusLabels[r.Status]; s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

// kind traduce formato + país al tipo que usa la gente: un MANGA coreano
// es un manhwa y uno chino o taiwanés, un manhua.
func kind(format, country string) string {
	switch format {
	case "MANGA":
		switch country {
		case "KR":
			return "manhwa"
		case "CN", "TW":
			return "manhua"
		}
		return "manga"
	case "ONE_SHOT":
		return "one-shot"
	case "NOVEL":
		return "novela"
	}
	return ""
}

var statusLabels = map[string]string{
	"RELEASING":        "en emisión",
	"FINISHED":         "terminado",
	"HIATUS":           "en pausa",
	"CANCELLED":        "cancelado",
	"NOT_YET_RELEASED": "próximamente",
}
