package library

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type entryState struct {
	status, notes, sourceURL string
	favorite                 int
}

func readEntryState(t *testing.T, db *sql.DB, id int64) entryState {
	t.Helper()
	var s entryState
	var notes, src sql.NullString
	if err := db.QueryRow(`SELECT status, notes, source_url, is_favorite FROM library_entries WHERE id = ?`, id).
		Scan(&s.status, &notes, &src, &s.favorite); err != nil {
		t.Fatal(err)
	}
	s.notes, s.sourceURL = notes.String, src.String
	return s
}

// postManage llama al handler de un endpoint del detalle con el form dado.
func postManage(h *Handler, fn func(http.ResponseWriter, *http.Request), entryID, userID int64, action string, form url.Values) *httptest.ResponseRecorder {
	id := strconv.FormatInt(entryID, 10)
	r := authReq("POST", "/library/entry/"+id+"/"+action, form.Encode(), userID)
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	fn(w, r)
	return w
}

func seedManagedEntry(t *testing.T, db *sql.DB, uid int64) int64 {
	t.Helper()
	entryID, _ := insertMangaWithChapters(t, db, uid, "matched")
	if _, err := db.Exec(`UPDATE library_entries SET notes = 'x', source_url = 'https://ej.com', is_favorite = 1 WHERE id = ?`, entryID); err != nil {
		t.Fatal(err)
	}
	return entryID
}

func TestEntryStatusSetsOnlyStatus(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	id := seedManagedEntry(t, db, uid)

	w := postManage(h, h.entryStatus, id, uid, "status", url.Values{"status": {"completed"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: esperado 303, obtenido %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/library/entry/"+strconv.FormatInt(id, 10)+"#estado" {
		t.Errorf("Location = %q", loc)
	}
	got := readEntryState(t, db, id)
	if got != (entryState{status: "completed", notes: "x", sourceURL: "https://ej.com", favorite: 1}) {
		t.Errorf("estado tras POST /status: %+v", got)
	}
}

func TestEntryStatusRejectsInvalid(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	id := seedManagedEntry(t, db, uid)

	w := postManage(h, h.entryStatus, id, uid, "status", url.Values{"status": {"foo"}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: esperado 400, obtenido %d", w.Code)
	}
	if got := readEntryState(t, db, id).status; got != "reading" {
		t.Errorf("status cambió a %q", got)
	}
}

func TestEntryFavoriteToggles(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	id := seedManagedEntry(t, db, uid)

	w := postManage(h, h.entryFavorite, id, uid, "favorite", url.Values{"favorite": {"0"}})
	if got := readEntryState(t, db, id); got.favorite != 0 || got.notes != "x" || got.status != "reading" {
		t.Errorf("favorite=0: %+v", got)
	}
	if loc := w.Header().Get("Location"); !strings.HasSuffix(loc, "#estado") {
		t.Errorf("Location = %q", loc)
	}
	postManage(h, h.entryFavorite, id, uid, "favorite", url.Values{"favorite": {"1"}})
	if got := readEntryState(t, db, id).favorite; got != 1 {
		t.Errorf("favorite=1: quedó %d", got)
	}
	if w := postManage(h, h.entryFavorite, id, uid, "favorite", url.Values{"favorite": {"si"}}); w.Code != http.StatusBadRequest {
		t.Errorf("favorite inválido: esperado 400, obtenido %d", w.Code)
	}
}

func TestEntryNotesSetsNotesAndURL(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	id := seedManagedEntry(t, db, uid)

	w := postManage(h, h.entryNotes, id, uid, "notes", url.Values{"notes": {"  nuevas  "}, "source_url": {"https://otra.com"}})
	if loc := w.Header().Get("Location"); !strings.HasSuffix(loc, "#notas") {
		t.Errorf("Location = %q", loc)
	}
	got := readEntryState(t, db, id)
	if got != (entryState{status: "reading", notes: "nuevas", sourceURL: "https://otra.com", favorite: 1}) {
		t.Errorf("estado tras POST /notes: %+v", got)
	}
}

func TestManageEndpointsScopedToOwner(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	alice := insertUser(t, db, "alice@example.com")
	bob := insertUser(t, db, "bob@example.com")
	id := seedManagedEntry(t, db, alice)
	before := readEntryState(t, db, id)

	for name, call := range map[string]func() *httptest.ResponseRecorder{
		"status":   func() *httptest.ResponseRecorder { return postManage(h, h.entryStatus, id, bob, "status", url.Values{"status": {"completed"}}) },
		"favorite": func() *httptest.ResponseRecorder { return postManage(h, h.entryFavorite, id, bob, "favorite", url.Values{"favorite": {"0"}}) },
		"notes":    func() *httptest.ResponseRecorder { return postManage(h, h.entryNotes, id, bob, "notes", url.Values{"notes": {"hack"}}) },
	} {
		if w := call(); w.Code != http.StatusNotFound {
			t.Errorf("%s con sesión de bob: esperado 404, obtenido %d", name, w.Code)
		}
	}
	if after := readEntryState(t, db, id); after != before {
		t.Errorf("la entry de alice cambió: %+v → %+v", before, after)
	}
}

func TestListRowsLinkToDetail(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	id, _ := insertMangaWithChapters(t, db, uid, "matched")
	body := renderList(t, h, uid, "/library")

	if !strings.Contains(body, `<a class="manga-row-link" href="/library/entry/`+strconv.FormatInt(id, 10)+`">Berserk</a>`) {
		t.Error("la fila no tiene un link al detalle que envuelva solo el título")
	}
	for _, bad := range []string{"<details", "/update"} {
		if strings.Contains(body, bad) {
			t.Errorf("la lista todavía tiene %q", bad)
		}
	}
}

func renderEntry(t *testing.T, h *Handler, entryID, userID int64) string {
	t.Helper()
	id := strconv.FormatInt(entryID, 10)
	r := authReq("GET", "/library/entry/"+id, "", userID)
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.entryDetail(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("detalle: status %d", w.Code)
	}
	return w.Body.String()
}

func TestEntryStatusButtonsPressed(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	id, _ := insertMangaWithChapters(t, db, uid, "matched")
	if _, err := db.Exec(`UPDATE library_entries SET status = 'planned' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	body := renderEntry(t, h, id, uid)

	if !strings.Contains(body, `role="group" aria-labelledby="estado-label"`) {
		t.Error("los botones de estado no están en un group con nombre")
	}
	re := regexp.MustCompile(`<button[^>]*name="status" value="(\w+)"[^>]*aria-pressed="(true|false)"`)
	pressed := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		pressed[m[1]] = m[2]
	}
	want := map[string]string{"reading": "false", "planned": "true", "completed": "false"}
	for k, v := range want {
		if pressed[k] != v {
			t.Errorf("botón %s: aria-pressed=%q, esperado %q (todos: %v)", k, pressed[k], v, pressed)
		}
	}
}

func TestEntryFavoritePressed(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	id, _ := insertMangaWithChapters(t, db, uid, "matched")
	re := regexp.MustCompile(`<button[^>]*name="favorite" value="([01])"[^>]*aria-pressed="(true|false)"`)

	m := re.FindStringSubmatch(renderEntry(t, h, id, uid))
	if m == nil || m[1] != "1" || m[2] != "false" {
		t.Errorf("no favorito: esperado value=1 aria-pressed=false, obtenido %v", m)
	}
	if _, err := db.Exec(`UPDATE library_entries SET is_favorite = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	m = re.FindStringSubmatch(renderEntry(t, h, id, uid))
	if m == nil || m[1] != "0" || m[2] != "true" {
		t.Errorf("favorito: esperado value=0 aria-pressed=true, obtenido %v", m)
	}
}

func TestEntryDeleteTwoSteps(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	id, _ := insertMangaWithChapters(t, db, uid, "matched")
	body := renderEntry(t, h, id, uid)

	i := strings.Index(body, `<details class="delete-confirm">`)
	j := strings.Index(body, `action="/library/`+strconv.FormatInt(id, 10)+`/delete"`)
	if i < 0 || j < i {
		t.Error("el form de borrar no está dentro del <details> de confirmación")
	}
}
