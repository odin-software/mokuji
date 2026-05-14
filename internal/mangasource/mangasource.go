// Package mangasource define la interfaz de las fuentes externas de
// metadata de manga (AniList, MangaDex, etc.) y los tipos que esas fuentes
// retornan.
//
// El MVP solo tiene la implementación de AniList en mangasource/anilist.
// MangaDex llega en fase 2 cuando agreguemos release tracking, sin cambios
// al package consumidor (internal/library).
package mangasource

import (
	"context"
	"errors"
)

// Source abstrae una fuente externa de metadata de manga. Cada
// implementación reporta su Name (que se persiste en manga.source) y provee
// búsqueda por query.
type Source interface {
	// Name retorna el identificador de la fuente. Se guarda en manga.source
	// para que dado un manga sepamos de qué API vino.
	Name() string

	// Search busca manga por título o términos relacionados. Retorna lo
	// suficiente para renderizar cards de resultados (el detalle completo
	// no es necesario en MVP).
	Search(ctx context.Context, query string) ([]SearchResult, error)
}

// SearchResult es el resumen de un manga retornado por Search.
type SearchResult struct {
	// SourceID es el identificador del manga en la fuente externa.
	// Stringificado porque no todas las APIs usan ints (AniList sí, MangaDex
	// usa UUIDs).
	SourceID string
	// Title es el título preferido para mostrar (typically english, fallback
	// romaji, fallback native).
	Title string
	// CoverURL apunta a la imagen del cover. La servimos como link directo
	// al CDN de la fuente — no proxeamos por mokuji.
	CoverURL string
	// Description ya limpia de HTML, lista para mostrar.
	Description string
}

// ErrNotFound se retorna cuando la fuente confirma que no existe el manga
// con ese SourceID.
var ErrNotFound = errors.New("manga no encontrado en la fuente")
