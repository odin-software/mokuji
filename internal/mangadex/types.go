package mangadex

import "time"

// MangaResult es un manga devuelto por Search.
//
// Title contiene el "título preferido" (inglés si existe, sino el primero
// disponible). Titles contiene todos los títulos conocidos (title +
// altTitles en cualquier idioma) para que el matcher pueda comparar
// contra los títulos de AniList sin importar el idioma.
type MangaResult struct {
	ID       string   // UUID de MangaDex
	Title    string   // preferido para mostrar al usuario
	Titles   []string // todos los títulos conocidos (incluido Title)
	CoverURL string   // URL del cover si vino includes[]=cover_art
	Year     int
	Status   string // ongoing|completed|hiatus|cancelled
}

// Chapter es un capítulo de un manga publicado en MangaDex.
//
// Number es string porque MangaDex acepta valores no numéricos
// ("125.5", "Special", o vacío para extras sin numeración).
//
// ExternalURL está poblado cuando el capítulo vive fuera de MangaDex
// (típicamente Mangaplus, Comikey, etc., para releases oficiales).
// Usar ReaderURL() para obtener la URL final que linkeamos al usuario.
type Chapter struct {
	ID          string
	Number      string
	Title       string
	Language    string
	PublishedAt time.Time
	ExternalURL string
}

// ReaderURL devuelve la URL final del lector. Si el capítulo tiene
// ExternalURL (release oficial en sitio externo) la usa. Sino construye
// la URL del reader de mangadex.org.
func (c Chapter) ReaderURL() string {
	if c.ExternalURL != "" {
		return c.ExternalURL
	}
	return "https://mangadex.org/chapter/" + c.ID
}
