package profile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// exportFormatVersion identifica el shape del JSON exportado. Si cambia el
// schema, bumpear este número — herramientas downstream pueden detectarlo.
const exportFormatVersion = 1

// exportData es el shape del archivo JSON descargado por /settings/export.
// Cualquier campo agregado en el futuro debe ser opcional (omitempty) para
// que un parser viejo siga funcionando contra archivos nuevos.
type exportData struct {
	ExportedAt    time.Time      `json:"exported_at"`
	FormatVersion int            `json:"format_version"`
	Profile       exportProfile  `json:"profile"`
	Library       []exportEntry  `json:"library"`
}

type exportProfile struct {
	ID                  int64     `json:"id"`
	Email               string    `json:"email"`
	CreatedAt           time.Time `json:"created_at"`
	ListVisibility      string    `json:"list_visibility"`
	EmailDigestEnabled  bool      `json:"email_digest_enabled"`
	PreferredLanguage   string    `json:"preferred_language"`
}

type exportEntry struct {
	Manga                 exportManga `json:"manga"`
	Status                string      `json:"status"`
	IsFavorite            bool        `json:"is_favorite"`
	SourceURL             string      `json:"source_url,omitempty"`
	Notes                 string      `json:"notes,omitempty"`
	CreatedAt             time.Time   `json:"created_at"`
	UpdatedAt             time.Time   `json:"updated_at"`
	// LastReadChapterNumber es el cursor de lectura como número (cap 70,
	// 70.5, etc). Reemplazó al puntero por chapter_id de v0.2.0 con la
	// migration 0008 — el cursor es por número, no por row, para que
	// funcione con multi-source donde varias filas representan el mismo cap.
	LastReadChapterNumber float64 `json:"last_read_chapter_number,omitempty"`
}

type exportManga struct {
	ID       int64                `json:"id"`
	Title    string               `json:"title"`
	Source   string               `json:"source"`
	SourceID string               `json:"source_id"`
	CoverURL string               `json:"cover_url,omitempty"`
	Sources  []exportMangaSource  `json:"sources,omitempty"`
}

// exportMangaSource es una fila de manga_sources que reportamos al user
// en el export — por dónde mokuji está siguiendo el manga (MangaDex,
// Asura, Comick…). Util para portabilidad: re-importar a otra instancia
// regenera el matching sin re-buscar.
type exportMangaSource struct {
	Source   string `json:"source"`
	SourceID string `json:"source_id,omitempty"`
	Status   string `json:"status"`
}

// exportHandler sirve GET /settings/export: devuelve un JSON descargable con
// todo el contenido propio del usuario logueado (perfil + biblioteca + cursor
// de lectura). Cumple GDPR Art. 15 (derecho de acceso).
//
// File-over-app: el archivo es self-contained y se puede inspeccionar con
// cualquier editor. Encoding pretty-printed (Indent) para que sea legible
// directo desde un descargador del browser.
func (h *Handler) exportData(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	profile, err := h.loadProfile(r.Context(), s.UserID)
	if err != nil {
		h.logger.Error("export: cargar perfil", "user_id", s.UserID, "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	entries, err := h.loadExportLibrary(r.Context(), s.UserID)
	if err != nil {
		h.logger.Error("export: cargar biblioteca", "user_id", s.UserID, "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	out := exportData{
		ExportedAt:    time.Now().UTC(),
		FormatVersion: exportFormatVersion,
		Profile:       profile,
		Library:       entries,
	}

	filename := fmt.Sprintf("mokuji-export-%s.json", out.ExportedAt.Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		h.logger.Error("export: encode", "err", err)
	}
}

func (h *Handler) loadProfile(ctx context.Context, userID int64) (exportProfile, error) {
	var (
		p             exportProfile
		createdAt     int64
		digestEnabled int
	)
	err := h.db.QueryRowContext(ctx, `
		SELECT id, email, created_at, list_visibility,
		       email_digest_enabled, preferred_language
		FROM users
		WHERE id = ?
	`, userID).Scan(
		&p.ID, &p.Email, &createdAt, &p.ListVisibility,
		&digestEnabled, &p.PreferredLanguage,
	)
	if err != nil {
		return exportProfile{}, err
	}
	p.CreatedAt = time.Unix(createdAt, 0).UTC()
	p.EmailDigestEnabled = digestEnabled == 1
	return p, nil
}

func (h *Handler) loadExportLibrary(ctx context.Context, userID int64) ([]exportEntry, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT m.id, m.title, m.source, m.source_id, m.cover_url,
		       le.status, le.is_favorite, le.source_url, le.notes,
		       le.created_at, le.updated_at, le.last_read_chapter_number
		FROM library_entries le
		JOIN manga m ON m.id = le.manga_id
		WHERE le.user_id = ?
		ORDER BY le.created_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byMangaID := map[int64]int{} // manga_id -> index en out (para attach de sources)
	var out []exportEntry
	for rows.Next() {
		var (
			e            exportEntry
			coverURL     sql.NullString
			sourceURL    sql.NullString
			notes        sql.NullString
			isFavorite   int
			createdAt    int64
			updatedAt    int64
			cursorNumber sql.NullFloat64
		)
		if err := rows.Scan(
			&e.Manga.ID, &e.Manga.Title, &e.Manga.Source, &e.Manga.SourceID,
			&coverURL,
			&e.Status, &isFavorite, &sourceURL, &notes,
			&createdAt, &updatedAt, &cursorNumber,
		); err != nil {
			return nil, err
		}
		e.Manga.CoverURL = coverURL.String
		e.SourceURL = sourceURL.String
		e.Notes = notes.String
		e.IsFavorite = isFavorite == 1
		e.CreatedAt = time.Unix(createdAt, 0).UTC()
		e.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		if cursorNumber.Valid {
			e.LastReadChapterNumber = cursorNumber.Float64
		}
		byMangaID[e.Manga.ID] = len(out)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Segundo query: cargar manga_sources de los mangas que el user tiene
	// en biblioteca. Una sola query con scope por user, attach por mangaID
	// para evitar N+1. Si el user no tiene biblioteca, byMangaID es vacío
	// y se saltea.
	if len(byMangaID) == 0 {
		return out, nil
	}
	srcRows, err := h.db.QueryContext(ctx, `
		SELECT ms.manga_id, ms.source, ms.source_id, ms.status
		FROM manga_sources ms
		JOIN library_entries le ON le.manga_id = ms.manga_id
		WHERE le.user_id = ?
	`, userID)
	if err != nil {
		return nil, err
	}
	defer srcRows.Close()

	for srcRows.Next() {
		var (
			mangaID  int64
			source   string
			sourceID string
			status   string
		)
		if err := srcRows.Scan(&mangaID, &source, &sourceID, &status); err != nil {
			return nil, err
		}
		idx, ok := byMangaID[mangaID]
		if !ok {
			continue
		}
		out[idx].Manga.Sources = append(out[idx].Manga.Sources, exportMangaSource{
			Source:   source,
			SourceID: sourceID,
			Status:   status,
		})
	}
	return out, srcRows.Err()
}
