package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"git.odin.do/kengru/mokuji/internal/flash"
	"git.odin.do/kengru/mokuji/internal/malimport"
	"git.odin.do/kengru/mokuji/internal/mangasource"
	"git.odin.do/kengru/mokuji/internal/session"
)

// Import de MyAnimeList. Diseño: docs/superpowers/specs/2026-10-19-import-mal-design.md.
//
// Regla central (§6): la biblioteca actual del user manda. Si el manga ya
// está, el import solo puede SUBIR el cursor; estado, notas, favorito y URL
// quedan como el user los dejó.

// ImportSummary es lo que ve el user al terminar el import.
type ImportSummary struct {
	Reading, Planned, Completed int      // entries nuevas, por estado
	Updated, Unchanged          int      // ya existían: cursor subió / nada cambió
	Dropped                     int      // abandonados en MAL, no se importan
	NotFound                    []string // títulos de MAL sin equivalente en AniList
}

// Imported es el total de entries nuevas.
func (s ImportSummary) Imported() int { return s.Reading + s.Planned + s.Completed }

// ImportedManga es una entry nueva en "leyendo": la que hay que matchear
// contra las sources después del commit.
type ImportedManga struct {
	MangaID int64
	Title   string
}

// applyMALImport escribe el import en una sola transacción: o entra todo o
// nada. found es el resultado de AniList por MAL id (los que faltan van a
// NotFound; no se adivina por título).
func (h *Handler) applyMALImport(ctx context.Context, userID int64, entries []malimport.Entry, found map[int]mangasource.SearchResult) (ImportSummary, []ImportedManga, error) {
	var sum ImportSummary
	var newReading []ImportedManga

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return sum, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Unix()
	for _, e := range entries {
		if e.Status == "" {
			sum.Dropped++
			continue
		}
		r, ok := found[e.MALID]
		if !ok {
			sum.NotFound = append(sum.NotFound, e.Title)
			continue
		}

		mangaID, err := upsertMangaWith(ctx, tx, h.src.Name(), r.SourceID, r.Title, r.CoverURL)
		if err != nil {
			return ImportSummary{}, nil, fmt.Errorf("upsert manga %s: %w", r.SourceID, err)
		}

		var (
			entryID int64
			cursor  sql.NullFloat64
		)
		err = tx.QueryRowContext(ctx,
			`SELECT id, last_read_chapter_number FROM library_entries WHERE user_id = ? AND manga_id = ?`,
			userID, mangaID,
		).Scan(&entryID, &cursor)

		switch {
		case errors.Is(err, sql.ErrNoRows):
			var newCursor any // NULL si MAL no trae progreso
			if e.ReadChapters > 0 {
				newCursor = e.ReadChapters
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO library_entries (user_id, manga_id, status, last_read_chapter_number, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)
			`, userID, mangaID, e.Status, newCursor, now, now); err != nil {
				return ImportSummary{}, nil, fmt.Errorf("crear entry: %w", err)
			}
			switch e.Status {
			case "reading":
				sum.Reading++
				newReading = append(newReading, ImportedManga{MangaID: mangaID, Title: r.Title})
			case "completed":
				sum.Completed++
			default:
				sum.Planned++
			}
		case err != nil:
			return ImportSummary{}, nil, fmt.Errorf("buscar entry: %w", err)
		case e.ReadChapters > cursor.Float64:
			if _, err := tx.ExecContext(ctx,
				`UPDATE library_entries SET last_read_chapter_number = ?, updated_at = ? WHERE id = ?`,
				e.ReadChapters, now, entryID,
			); err != nil {
				return ImportSummary{}, nil, fmt.Errorf("subir cursor: %w", err)
			}
			sum.Updated++
		default:
			sum.Unchanged++
		}
	}

	if err := tx.Commit(); err != nil {
		return ImportSummary{}, nil, err
	}
	return sum, newReading, nil
}

const (
	// maxImportFileBytes es el tope del archivo subido (spec §2). El body
	// completo ya viene acotado por csrf.MaxMultipartBytes.
	maxImportFileBytes = 5 << 20
	// importLimitEvery/Burst: 3 imports por hora por user (spec §8).
	importLimitEvery = 20 * time.Minute
	importLimitBurst = 3
	// importWriteDeadline extiende el WriteTimeout de 30 s del server para
	// este handler; nginx corta a los 60 (spec §7).
	importWriteDeadline = 55 * time.Second
)

// Mensajes de error del import (spec §10, copy en tuteo).
const (
	msgImportNoFile      = "elige el archivo del export de MAL."
	msgImportInvalid     = "ese archivo no parece un export de MyAnimeList."
	msgImportAnime       = "ese es tu export de anime; necesitamos el de manga."
	msgImportTooLarge    = "el archivo es demasiado grande (máximo 5 MB)."
	msgImportTooMany     = "tu lista tiene más de 1000 mangas; por ahora el import llega hasta ahí."
	msgImportCatalogDown = "AniList está ocupado o no responde. intenta en unos minutos."
	msgImportRateLimited = "ya importaste varias veces seguidas. intenta en un rato."
)

type importResultData struct {
	Session *session.Session
	Summary ImportSummary
}

// importMAL sirve POST /settings/import/mal: parsea el export, resuelve los
// MAL ids en AniList y recién entonces escribe (todo o nada). Responde con
// la página de resultado directo, sin PRG: reimportar es idempotente y el
// resumen no cabe en un flash (spec §10).
func (h *Handler) importMAL(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.importLimiter.Allow(strconv.FormatInt(s.UserID, 10)) {
		h.importError(w, r, msgImportRateLimited)
		return
	}
	// El default de 30 s no alcanza para una lista grande; si el writer no
	// soporta deadlines (tests), seguimos igual.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(importWriteDeadline))

	file, header, err := r.FormFile("file")
	if err != nil {
		h.importError(w, r, msgImportNoFile)
		return
	}
	defer file.Close()
	if header.Size > maxImportFileBytes {
		h.importError(w, r, msgImportTooLarge)
		return
	}

	entries, err := malimport.Parse(file)
	if err != nil {
		h.importError(w, r, parseErrorMessage(err))
		return
	}

	ids := make([]int, 0, len(entries))
	for _, e := range entries {
		if e.Status != "" { // los Dropped no se importan: no hace falta buscarlos
			ids = append(ids, e.MALID)
		}
	}
	found, err := h.malResolver.ByMALIDs(r.Context(), ids)
	if err != nil {
		h.logger.Warn("import MAL: resolver ids", "user_id", s.UserID, "ids", len(ids), "err", err)
		h.importError(w, r, msgImportCatalogDown)
		return
	}

	sum, _, err := h.applyMALImport(r.Context(), s.UserID, entries, found)
	if err != nil {
		h.logger.Error("import MAL: aplicar", "user_id", s.UserID, "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	// Solo conteos en logs, nunca títulos (spec §11).
	h.logger.Info("import MAL",
		"user_id", s.UserID, "entries", len(entries), "imported", sum.Imported(),
		"updated", sum.Updated, "unchanged", sum.Unchanged,
		"dropped", sum.Dropped, "not_found", len(sum.NotFound))

	h.render(w, r, "library/import-result.html", importResultData{Session: s, Summary: sum})
}

func (h *Handler) importError(w http.ResponseWriter, r *http.Request, msg string) {
	if h.flasher != nil {
		h.flasher.Set(w, flash.KindError, msg)
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func parseErrorMessage(err error) string {
	switch {
	case errors.Is(err, malimport.ErrAnimeExport):
		return msgImportAnime
	case errors.Is(err, malimport.ErrTooLarge):
		return msgImportTooLarge
	case errors.Is(err, malimport.ErrTooManyEntries):
		return msgImportTooMany
	default:
		return msgImportInvalid
	}
}
