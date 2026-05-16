// Package releasesource define la interfaz que toda fuente de release
// tracking debe satisfacer (MangaDex, Asura, Comick, …) y los tipos
// compartidos que esas fuentes devuelven.
//
// El worker y el matcher consumen esta abstracción — no conocen ni
// importan los clientes concretos. main.go wirea un map[string]Client por
// nombre de source y los inyecta. ADR 0014.
package releasesource

import (
	"context"
	"errors"
	"time"
)

// ErrRateLimited indica que la source pidió ralentizar (HTTP 429 después
// de los retries internos del cliente). El caller — típicamente el
// worker — reagenda la operación más tarde en vez de tratarla como
// error fatal.
var ErrRateLimited = errors.New("releasesource: rate limited")

// ErrNotFound indica que la source confirma que el recurso solicitado
// (manga, chapter) no existe.
var ErrNotFound = errors.New("releasesource: not found")

// Client es el contrato que cumple toda source. Las implementaciones
// concretas viven en su propio paquete (internal/mangadex, internal/asura,
// etc.) y se registran por Name() en el map del worker.
type Client interface {
	// Name es el identificador estable de la source: 'mangadex', 'asura',
	// 'comick', ... Se persiste en manga_sources.source y se usa como
	// llave del map del worker. Debe ser único y no cambiar entre
	// releases (cambiarlo orfanaría filas existentes).
	Name() string

	// Search busca manga por título. Devuelve hasta N candidatos
	// ordenados por relevancia de la source. El matcher consume estos
	// resultados para decidir matched/needs_review/no_results.
	Search(ctx context.Context, title string) ([]MangaResult, error)

	// FetchChapters trae los capítulos del manga en el idioma dado,
	// filtrados a los publicados después de since (zero = traer todo).
	// El worker es quien decide qué since pasar (max(published_at) + 1s
	// de lo que ya tiene en DB).
	FetchChapters(ctx context.Context, sourceID, language string, since time.Time) ([]Chapter, error)
}

// MangaResult es un candidato devuelto por Search.
//
// Title es el título preferido para mostrar al user. Titles es la lista
// completa de variantes (idiomas/alt-titles) que el matcher usa para
// comparar contra los inputs sin importar el idioma del query.
type MangaResult struct {
	ID       string   // ID o slug específico de la source
	Title    string   // título preferido (típicamente inglés)
	Titles   []string // todos los títulos conocidos (incluyendo Title)
	CoverURL string   // URL absoluta del cover; sin proxy
	Year     int
	Status   string // ongoing|completed|hiatus|cancelled (semántica por source)
}

// Chapter es un capítulo publicado en la source.
//
// Number es string porque las sources aceptan valores no-numéricos
// ("125.5", "Special", vacío para extras). ExternalURL se llena cuando
// la source apunta a un lector externo (típicamente releases oficiales).
type Chapter struct {
	ID          string
	Number      string
	Title       string
	Language    string
	PublishedAt time.Time
	ExternalURL string
}
