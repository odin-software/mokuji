package library

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Endpoints chicos del detalle (D2). Cada uno toca un solo campo, así un
// botón no pisa lo demás (el viejo POST /library/{id}/update sobrescribía
// todo el form). Todos redirigen al detalle con un fragmento al bloque que
// los contiene: el foco de teclado sigue ahí después del POST en vez de
// volver al logo.

func (h *Handler) entryStatus(w http.ResponseWriter, r *http.Request) {
	h.updateEntryField(w, r, "estado", func(r *http.Request) (string, []any, bool) {
		status := r.PostFormValue("status")
		if !validStatuses[status] {
			return "", nil, false
		}
		return "status = ?", []any{status}, true
	})
}

func (h *Handler) entryFavorite(w http.ResponseWriter, r *http.Request) {
	h.updateEntryField(w, r, "estado", func(r *http.Request) (string, []any, bool) {
		switch r.PostFormValue("favorite") {
		case "1":
			return "is_favorite = ?", []any{1}, true
		case "0":
			return "is_favorite = ?", []any{0}, true
		}
		return "", nil, false
	})
}

func (h *Handler) entryNotes(w http.ResponseWriter, r *http.Request) {
	h.updateEntryField(w, r, "notas", func(r *http.Request) (string, []any, bool) {
		notes := strings.TrimSpace(r.PostFormValue("notes"))
		if len(notes) > maxNotes {
			notes = notes[:maxNotes]
		}
		sourceURL := strings.TrimSpace(r.PostFormValue("source_url"))
		if len(sourceURL) > maxSourceURL {
			sourceURL = sourceURL[:maxSourceURL]
		}
		return "notes = ?, source_url = ?", []any{notes, sourceURL}, true
	})
}

// updateEntryField es el esqueleto común: sesión, id, form, UPDATE scoped
// por user y redirect con fragmento. parse devuelve el SET y sus args, o
// false para responder 400 sin tocar nada.
func (h *Handler) updateEntryField(w http.ResponseWriter, r *http.Request, fragment string, parse func(*http.Request) (string, []any, bool)) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	set, args, ok := parse(r)
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	args = append(args, time.Now().UTC().Unix(), id, s.UserID)
	res, err := h.db.ExecContext(r.Context(),
		`UPDATE library_entries SET `+set+`, updated_at = ? WHERE id = ? AND user_id = ?`, args...)
	if err != nil {
		h.logger.Error("actualizar entry", "err", err, "entry_id", id)
		h.errs.InternalServerError(w, r)
		return
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/library/entry/%d#%s", id, fragment), http.StatusSeeOther)
}
