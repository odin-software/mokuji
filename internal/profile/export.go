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
	Manga             exportManga    `json:"manga"`
	Status            string         `json:"status"`
	LastChapter       string         `json:"last_chapter,omitempty"`
	IsFavorite        bool           `json:"is_favorite"`
	SourceURL         string         `json:"source_url,omitempty"`
	Notes             string         `json:"notes,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	LastReadChapter   *exportChapter `json:"last_read_chapter,omitempty"`
}

type exportManga struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Source     string `json:"source"`
	SourceID   string `json:"source_id"`
	CoverURL   string `json:"cover_url,omitempty"`
	MangadexID string `json:"mangadex_id,omitempty"`
}

type exportChapter struct {
	ID          int64     `json:"id"`
	Number      string    `json:"number,omitempty"`
	Title       string    `json:"title,omitempty"`
	Language    string    `json:"language,omitempty"`
	ExternalURL string    `json:"external_url,omitempty"`
	PublishedAt time.Time `json:"published_at,omitzero"`
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
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	entries, err := h.loadExportLibrary(r.Context(), s.UserID)
	if err != nil {
		h.logger.Error("export: cargar biblioteca", "user_id", s.UserID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
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
		SELECT m.id, m.title, m.source, m.source_id, m.cover_url, m.mangadex_id,
		       le.status, le.last_chapter, le.is_favorite, le.source_url, le.notes,
		       le.created_at, le.updated_at,
		       cur.id, cur.number, cur.title, cur.language, cur.external_url, cur.published_at
		FROM library_entries le
		JOIN manga m ON m.id = le.manga_id
		LEFT JOIN chapter cur ON cur.id = le.last_read_chapter_id
		WHERE le.user_id = ?
		ORDER BY le.created_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []exportEntry
	for rows.Next() {
		var (
			e               exportEntry
			coverURL        sql.NullString
			mangadexID      sql.NullString
			lastChapter     sql.NullString
			sourceURL       sql.NullString
			notes           sql.NullString
			isFavorite      int
			createdAt       int64
			updatedAt       int64
			curID           sql.NullInt64
			curNumber       sql.NullString
			curTitle        sql.NullString
			curLanguage     sql.NullString
			curExternalURL  sql.NullString
			curPublishedAt  sql.NullInt64
		)
		if err := rows.Scan(
			&e.Manga.ID, &e.Manga.Title, &e.Manga.Source, &e.Manga.SourceID,
			&coverURL, &mangadexID,
			&e.Status, &lastChapter, &isFavorite, &sourceURL, &notes,
			&createdAt, &updatedAt,
			&curID, &curNumber, &curTitle, &curLanguage, &curExternalURL, &curPublishedAt,
		); err != nil {
			return nil, err
		}
		e.Manga.CoverURL = coverURL.String
		e.Manga.MangadexID = mangadexID.String
		e.LastChapter = lastChapter.String
		e.SourceURL = sourceURL.String
		e.Notes = notes.String
		e.IsFavorite = isFavorite == 1
		e.CreatedAt = time.Unix(createdAt, 0).UTC()
		e.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		if curID.Valid {
			c := &exportChapter{
				ID:          curID.Int64,
				Number:      curNumber.String,
				Title:       curTitle.String,
				Language:    curLanguage.String,
				ExternalURL: curExternalURL.String,
			}
			if curPublishedAt.Valid {
				c.PublishedAt = time.Unix(curPublishedAt.Int64, 0).UTC()
			}
			e.LastReadChapter = c
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
