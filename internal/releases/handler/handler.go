// Package handler sirve los endpoints relacionados con releases:
//   - GET /releases -> lista flat de capítulos no leídos del usuario,
//     ordenados por publishAt descendente (más nuevos arriba).
//
// "No leído" se define vía el cursor library_entries.last_read_chapter_id
// (ADR 0013 §9): cap.published_at > cursor.published_at, o todos los caps
// si el cursor es NULL. Filtra por status='reading' y por idioma preferido
// del usuario.
package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"git.odin.do/kengru/mokuji/internal/csrf"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/version"
)

// MaxReleases es el cap de la página. Sin paginación por ahora — si un
// usuario tiene >100 caps acumulados es indicador de "ponete al día" más
// que de UX nuestra.
const MaxReleases = 100

// Config agrupa las dependencias del handler.
type Config struct {
	DB            *sql.DB
	HTMLTemplates fs.FS
	Logger        *slog.Logger
}

// Handler sirve /releases.
type Handler struct {
	db     *sql.DB
	tmpl   *template.Template
	logger *slog.Logger
}

// Release es una fila de la página: un capítulo + metadata mínima del manga.
type Release struct {
	ChapterID     int64
	ChapterNumber string
	ChapterTitle  string
	Language      string
	PublishedAt   time.Time
	ReaderURL     string // ExternalURL si existe, sino mangadex.org/chapter/{source_id}
	MangaID       int64
	MangaTitle    string
	CoverURL      string
}

// New construye un Handler. Falla si templates no parsean.
func New(cfg Config) (*Handler, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	funcs := template.FuncMap{
		"csrfInput":  csrf.Input,
		"appVersion": func() string { return version.Value },
	}
	base, err := template.New("base").Funcs(funcs).ParseFS(cfg.HTMLTemplates, "layout.html")
	if err != nil {
		return nil, fmt.Errorf("parsear layout: %w", err)
	}
	page, err := base.Clone()
	if err != nil {
		return nil, fmt.Errorf("clonar base: %w", err)
	}
	if _, err := page.ParseFS(cfg.HTMLTemplates, "releases.html"); err != nil {
		return nil, fmt.Errorf("parsear releases.html: %w", err)
	}

	return &Handler{db: cfg.DB, tmpl: page, logger: cfg.Logger}, nil
}

// Mount registra los endpoints en el mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /releases", h.list)
	mux.HandleFunc("POST /releases/mark-read", h.markRead)
}

type pageData struct {
	Session  *session.Session
	Releases []Release
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}

	releases, err := h.unreadReleases(r.Context(), s.UserID, MaxReleases)
	if err != nil {
		h.logger.Error("listar releases", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "layout", pageData{
		Session:  s,
		Releases: releases,
	}); err != nil {
		h.logger.Error("renderizar releases", "err", err)
	}
}

// markRead avanza el cursor library_entries.last_read_chapter_id al cap
// indicado en el form. La query bloquea cualquier intento de marcar un
// cap que no pertenezca a un manga en la biblioteca del user (subselect
// devuelve NULL y la igualdad falsa => 0 rows affected).
func (h *Handler) markRead(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	chapterID, err := strconv.ParseInt(r.PostFormValue("chapter_id"), 10, 64)
	if err != nil || chapterID <= 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	res, err := h.db.ExecContext(r.Context(), `
		UPDATE library_entries
		SET last_read_chapter_id = ?, updated_at = ?
		WHERE user_id = ?
		  AND manga_id = (SELECT manga_id FROM chapter WHERE id = ?)
	`, chapterID, time.Now().UTC().Unix(), s.UserID, chapterID)
	if err != nil {
		h.logger.Error("marcar leído", "user_id", s.UserID, "chapter_id", chapterID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		http.NotFound(w, r)
		return
	}

	http.Redirect(w, r, "/releases", http.StatusSeeOther)
}

// unreadReleases devuelve los caps no leídos del usuario, ordenados por
// publishAt descendente. Filtros:
//   - solo entries del user con status='reading'
//   - solo caps en el idioma preferido del usuario
//   - solo caps publicados después del cursor (o todos si cursor NULL)
//
// Exportado para que el widget en home (en un commit posterior) reuse
// el mismo query con un limit más chico.
func (h *Handler) unreadReleases(ctx context.Context, userID int64, limit int) ([]Release, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT chapter.id, chapter.source_id, chapter.number, chapter.title, chapter.language,
		       chapter.published_at, chapter.external_url,
		       manga.id, manga.title, manga.cover_url
		FROM chapter
		JOIN manga          ON manga.id = chapter.manga_id
		JOIN library_entries le ON le.manga_id = manga.id
		JOIN users          ON users.id = le.user_id
		LEFT JOIN chapter cur ON cur.id = le.last_read_chapter_id
		WHERE le.user_id = ?
		  AND le.status = 'reading'
		  AND chapter.language = users.preferred_language
		  AND (cur.published_at IS NULL OR chapter.published_at > cur.published_at)
		ORDER BY chapter.published_at DESC
		LIMIT ?
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Release
	for rows.Next() {
		var (
			r           Release
			sourceID    string
			number      sql.NullString
			title       sql.NullString
			language    sql.NullString
			publishedAt sql.NullInt64
			externalURL sql.NullString
			coverURL    sql.NullString
		)
		if err := rows.Scan(
			&r.ChapterID, &sourceID, &number, &title, &language,
			&publishedAt, &externalURL,
			&r.MangaID, &r.MangaTitle, &coverURL,
		); err != nil {
			return nil, err
		}
		r.ChapterNumber = number.String
		r.ChapterTitle = title.String
		r.Language = language.String
		if publishedAt.Valid {
			r.PublishedAt = time.Unix(publishedAt.Int64, 0).UTC()
		}
		r.CoverURL = coverURL.String
		if externalURL.Valid && externalURL.String != "" {
			r.ReaderURL = externalURL.String
		} else {
			r.ReaderURL = "https://mangadex.org/chapter/" + sourceID
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
