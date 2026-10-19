package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"git.odin.do/kengru/mokuji/internal/malimport"
	"git.odin.do/kengru/mokuji/internal/mangasource"
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
