// Package onboarding sirve el wizard de primer uso post-OTP.
//
// Tres pasos:
//  1. Qué es mokuji (informativo, kanji 登).
//  2. Qué monitoreamos (informativo — MangaDex + Asura, kanji 新).
//  3. Digest prefs + visibilidad (form: digest_frequency, list_visibility, kanji 始).
//
// Al completar step 3, escribe users.onboarded_at = now() y digest_frequency,
// luego redirige a /library.
//
// Si users.onboarded_at IS NOT NULL al entrar a cualquier step, redirige a
// /library (idempotente).
package onboarding

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/odin-software/mokuji/internal/csrf"
	"github.com/odin-software/mokuji/internal/feedback"
	"github.com/odin-software/mokuji/internal/session"
	"github.com/odin-software/mokuji/internal/version"
)

// Config agrupa las dependencias del handler.
type Config struct {
	DB            *sql.DB
	HTMLTemplates fs.FS
	Logger        *slog.Logger
	// SourceNames son las sources habilitadas en esta instancia, para que
	// el paso 2 las nombre (D8) en vez de una lista escrita a mano.
	SourceNames []string
}

// Handler sirve el wizard de onboarding.
type Handler struct {
	db        *sql.DB
	tmplStep1 *template.Template
	tmplStep2 *template.Template
	tmplStep3 *template.Template
	logger    *slog.Logger
	sources   string
}

// New construye un Handler. Falla si las plantillas no parsean.
func New(cfg Config) (*Handler, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	funcs := template.FuncMap{
		"csrfInput":       csrf.Input,
		"appVersion":      func() string { return version.Value },
		"feedbackEnabled": feedback.IsEnabled,
	}
	parse := func(stepFile string) (*template.Template, error) {
		t, err := template.New("base").Funcs(funcs).ParseFS(
			cfg.HTMLTemplates, "layout.html", stepFile,
		)
		if err != nil {
			return nil, fmt.Errorf("parsear %s: %w", stepFile, err)
		}
		return t, nil
	}
	s1, err := parse("onboarding/step1.html")
	if err != nil {
		return nil, err
	}
	s2, err := parse("onboarding/step2.html")
	if err != nil {
		return nil, err
	}
	s3, err := parse("onboarding/step3.html")
	if err != nil {
		return nil, err
	}
	return &Handler{
		db:        cfg.DB,
		tmplStep1: s1,
		tmplStep2: s2,
		tmplStep3: s3,
		logger:    cfg.Logger,
		sources:   joinY(cfg.SourceNames),
	}, nil
}

// Mount registra los endpoints en el mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /onboarding", h.step1)
	mux.HandleFunc("GET /onboarding/step2", h.step2)
	mux.HandleFunc("GET /onboarding/step3", h.step3)
	mux.HandleFunc("POST /onboarding/finish", h.finish)
}

type stepData struct {
	Session *session.Session
	// Sources: "asura, comick y mangadex" (solo lo usa el paso 2).
	Sources string
}

// joinY une con comas y "y" antes del último. Misma regla que el resumen de
// sources del detalle (library), duplicada a propósito: son tres líneas y
// onboarding no debería depender de library.
func joinY(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " y " + items[len(items)-1]
}

// requireUnonboarded verifica que el usuario tenga sesión y que onboarded_at
// sea NULL. Si onboarded_at ya está seteado, redirige a /library.
func (h *Handler) requireUnonboarded(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return nil, false
	}
	var onboardedAt sql.NullInt64
	err := h.db.QueryRowContext(r.Context(),
		`SELECT onboarded_at FROM users WHERE id = ?`, s.UserID,
	).Scan(&onboardedAt)
	if err != nil {
		h.logger.Error("cargar onboarded_at", "user_id", s.UserID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if onboardedAt.Valid {
		http.Redirect(w, r, "/library", http.StatusSeeOther)
		return nil, false
	}
	return s, true
}

func (h *Handler) step1(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireUnonboarded(w, r)
	if !ok {
		return
	}
	h.render(w, h.tmplStep1, "step1", stepData{Session: s})
}

func (h *Handler) step2(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireUnonboarded(w, r)
	if !ok {
		return
	}
	h.render(w, h.tmplStep2, "step2", stepData{Session: s, Sources: h.sources})
}

func (h *Handler) step3(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireUnonboarded(w, r)
	if !ok {
		return
	}
	h.render(w, h.tmplStep3, "step3", stepData{Session: s})
}

func (h *Handler) finish(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireUnonboarded(w, r)
	if !ok {
		return
	}
	freq := r.PostFormValue("digest_frequency")
	if freq != "weekly" && freq != "daily" && freq != "off" {
		freq = "weekly"
	}
	// Sin valor válido se conserva el default de la columna (public).
	vis := r.PostFormValue("list_visibility")
	if vis != "public" && vis != "private" {
		vis = "public"
	}
	_, err := h.db.ExecContext(r.Context(),
		`UPDATE users SET digest_frequency = ?, list_visibility = ?, onboarded_at = ? WHERE id = ?`,
		freq, vis, time.Now().Unix(), s.UserID,
	)
	if err != nil {
		h.logger.Error("finish onboarding", "user_id", s.UserID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/library", http.StatusSeeOther)
}

func (h *Handler) render(w http.ResponseWriter, tmpl *template.Template, label string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("renderizar onboarding", "step", label, "err", err)
	}
}
