package library

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.odin.do/kengru/mokuji/internal/releases/matcher"
	"git.odin.do/kengru/mokuji/internal/session"
)

// insertManga + manga_sources row helper para los tests de este archivo.
// Devuelve (entryID, mangaID).
func insertEntryWithSource(t *testing.T, db *sql.DB, userID int64, mangadexSourceID, status string) (int64, int64) {
	t.Helper()
	mangaSeq++
	anilistID := "s-" + strconv.FormatInt(mangaSeq, 10)
	res, err := db.Exec(`INSERT INTO manga (source, source_id, title, created_at, updated_at) VALUES ('anilist', ?, 'M', ?, ?)`,
		anilistID, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	mangaID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO manga_sources (manga_id, source, source_id, status) VALUES (?, 'mangadex', ?, ?)`,
		mangaID, mangadexSourceID, status); err != nil {
		t.Fatal(err)
	}
	res, err = db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at) VALUES (?, ?, 'reading', ?, ?)`,
		userID, mangaID, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	entryID, _ := res.LastInsertId()
	return entryID, mangaID
}

// --- loadEntrySources ---

func TestLoadEntrySourcesUnionsRowsAndConfigured(t *testing.T) {
	// El handler tiene matchers cableados para mangadex + asura. La DB
	// solo tiene una fila para mangadex. loadEntrySources debe devolver
	// dos entries: mangadex con HasRow=true, asura con HasRow=false.
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	h.matchers = map[string]Matcher{
		"mangadex": &fakeMatcher{},
		"asura":    &fakeMatcher{},
	}
	uid := insertUser(t, db, "alice@example.com")
	_, mangaID := insertEntryWithSource(t, db, uid, "mdx-1", "matched")

	got, err := h.loadEntrySources(context.Background(), mangaID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("sources: esperado 2, obtenido %d (%+v)", len(got), got)
	}
	// orden alfabético: asura, mangadex
	if got[0].Source != "asura" || got[0].HasRow || got[0].Status != matcher.StatusUnmatched {
		t.Errorf("asura: %+v (esperaba sin fila, status unmatched)", got[0])
	}
	if got[1].Source != "mangadex" || !got[1].HasRow || got[1].Status != matcher.StatusMatched {
		t.Errorf("mangadex: %+v (esperaba con fila, status matched)", got[1])
	}
	if !got[0].Configured || !got[1].Configured {
		t.Error("ambas sources deberían marcar Configured=true")
	}
}

func TestLoadEntrySourcesFlagsDisabledRow(t *testing.T) {
	// Fila existente para una source que YA NO está en h.matchers
	// (env apagada después de un deploy). Debe aparecer con
	// HasRow=true, Configured=false.
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	h.matchers = map[string]Matcher{} // ninguna source cableada
	uid := insertUser(t, db, "alice@example.com")
	_, mangaID := insertEntryWithSource(t, db, uid, "mdx-1", "matched")

	got, err := h.loadEntrySources(context.Background(), mangaID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("sources: %d", len(got))
	}
	if got[0].HasRow != true || got[0].Configured != false {
		t.Errorf("source con fila pero sin matcher cableado: %+v", got[0])
	}
}

// --- entrySourceRetry ---

func authRetryReq(method, target string, userID int64, entryID int64, source string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	r.SetPathValue("source", source)
	return session.WithUserID(r, userID)
}

func TestEntrySourceRetryPersistsMatchedRow(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	h.matchers = map[string]Matcher{
		"asura": &fakeMatcher{result: matcher.Result{Status: matcher.StatusMatched, SourceID: "asura-slug"}},
	}
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertEntryWithSource(t, db, uid, "mdx-1", "matched")

	// Pre-condición: no hay fila asura todavía.
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM manga_sources WHERE manga_id = ? AND source = 'asura'`, mangaID).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Fatalf("pre: esperaba 0 filas asura, obtuve %d", cnt)
	}

	r := authRetryReq("POST", "/library/entry/"+strconv.FormatInt(entryID, 10)+"/sources/asura/retry", uid, entryID, "asura")
	w := httptest.NewRecorder()
	h.entrySourceRetry(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}

	// Verificar fila persistida.
	var status, sourceID string
	if err := db.QueryRow(`SELECT status, source_id FROM manga_sources WHERE manga_id = ? AND source = 'asura'`, mangaID).Scan(&status, &sourceID); err != nil {
		t.Fatal(err)
	}
	if status != "matched" || sourceID != "asura-slug" {
		t.Errorf("fila asura: status=%q source_id=%q", status, sourceID)
	}
}

func TestEntrySourceRetryRejectsUnknownSource(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	h.matchers = map[string]Matcher{} // ninguna cableada
	uid := insertUser(t, db, "alice@example.com")
	entryID, _ := insertEntryWithSource(t, db, uid, "mdx-1", "matched")

	r := authRetryReq("POST", "/library/entry/X/sources/asura/retry", uid, entryID, "asura")
	w := httptest.NewRecorder()
	h.entrySourceRetry(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: %d (esperaba 400)", w.Code)
	}
}

func TestEntrySourceRetryRejectsInvalidSourceName(t *testing.T) {
	// Source con caracteres no permitidos → 400.
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	h.matchers = map[string]Matcher{"asura": &fakeMatcher{}}
	uid := insertUser(t, db, "alice@example.com")
	entryID, _ := insertEntryWithSource(t, db, uid, "mdx-1", "matched")

	r := authRetryReq("POST", "/library/entry/X/sources/HACK';DROP/retry", uid, entryID, "HACK';DROP")
	w := httptest.NewRecorder()
	h.entrySourceRetry(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("source name inválido: status %d", w.Code)
	}
}

func TestEntrySourceRetry404OnOtherUserEntry(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	h.matchers = map[string]Matcher{"mangadex": &fakeMatcher{}}
	alice := insertUser(t, db, "alice@example.com")
	bob := insertUser(t, db, "bob@example.com")
	aliceEntry, _ := insertEntryWithSource(t, db, alice, "mdx-1", "matched")

	// Bob intenta operar sobre el entry de Alice.
	r := authRetryReq("POST", "/library/entry/X/sources/mangadex/retry", bob, aliceEntry, "mangadex")
	w := httptest.NewRecorder()
	h.entrySourceRetry(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: %d (esperaba 404)", w.Code)
	}
}

// --- entrySourceUnlink ---

func TestEntrySourceUnlinkRemovesRow(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	h.matchers = map[string]Matcher{"mangadex": &fakeMatcher{}}
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertEntryWithSource(t, db, uid, "mdx-1", "matched")

	r := authRetryReq("POST", "/library/entry/X/sources/mangadex/unlink", uid, entryID, "mangadex")
	w := httptest.NewRecorder()
	h.entrySourceUnlink(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: %d", w.Code)
	}

	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM manga_sources WHERE manga_id = ?`, mangaID).Scan(&cnt)
	if cnt != 0 {
		t.Errorf("la fila no fue eliminada: %d filas restantes", cnt)
	}
}

func TestEntrySourceUnlinkIsIdempotent(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	// Importa: no necesita matcher para unlink — solo borra la fila.
	uid := insertUser(t, db, "alice@example.com")
	entryID, _ := insertEntryWithSource(t, db, uid, "mdx-1", "matched")

	// Primer call borra; segundo call sobre la fila ya inexistente sigue siendo 303.
	for i := range 2 {
		r := authRetryReq("POST", "/library/entry/X/sources/mangadex/unlink", uid, entryID, "mangadex")
		w := httptest.NewRecorder()
		h.entrySourceUnlink(w, r)
		if w.Code != http.StatusSeeOther {
			t.Errorf("call %d: status %d", i, w.Code)
		}
	}
}

func TestEntrySourceUnlinkPreservesChapters(t *testing.T) {
	// Desvincular no debe borrar los caps ya bajados — solo dejar de
	// polleear. Esto es importante para que el user no pierda historial.
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	h.matchers = map[string]Matcher{"mangadex": &fakeMatcher{}}
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertEntryWithSource(t, db, uid, "mdx-1", "matched")
	if _, err := db.Exec(`INSERT INTO chapter (manga_id, source, source_id, number, language, published_at, created_at) VALUES (?, 'mangadex', 'ch-1', '1', 'en', ?, 0)`,
		mangaID, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	r := authRetryReq("POST", "/library/entry/X/sources/mangadex/unlink", uid, entryID, "mangadex")
	w := httptest.NewRecorder()
	h.entrySourceUnlink(w, r)

	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM chapter WHERE manga_id = ?`, mangaID).Scan(&cnt)
	if cnt != 1 {
		t.Errorf("caps borrados al desvincular: %d (esperaba 1)", cnt)
	}
}

// --- isValidSourceName ---

func TestIsValidSourceName(t *testing.T) {
	cases := map[string]bool{
		"mangadex":          true,
		"asura":             true,
		"comick":            true,
		"reaper-scans":      true,
		"reaper-scans-2":    true,
		"":                  false,
		"Mangadex":          false, // mayúsculas no
		"manga_dex":         false, // underscore no
		"manga dex":         false, // espacio no
		"';DROP TABLE x;--": false,
		"-leading-dash":     false,
		"1leading-digit":    false,
	}
	for in, want := range cases {
		if got := isValidSourceName(in); got != want {
			t.Errorf("isValidSourceName(%q): %v, want %v", in, got, want)
		}
	}
}

// --- detail page rendering ---

func TestEntryDetailRendersSourcesFieldset(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	h.matchers = map[string]Matcher{
		"mangadex": &fakeMatcher{},
		"asura":    &fakeMatcher{},
	}
	uid := insertUser(t, db, "alice@example.com")
	entryID, _ := insertEntryWithSource(t, db, uid, "mdx-1", "matched")

	r := authReq("GET", "/library/entry/"+strconv.FormatInt(entryID, 10), "", uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	body := w.Body.String()
	// Header del fieldset.
	if !strings.Contains(body, "sources-fieldset") {
		t.Error("falta el section sources-fieldset")
	}
	// mangadex con fila matched
	if !strings.Contains(body, ">mangadex<") || !strings.Contains(body, "✓ matched") {
		t.Error("falta mangadex matched en el render")
	}
	// asura sin fila — placeholder "sin intentar"
	if !strings.Contains(body, ">asura<") || !strings.Contains(body, "sin intentar") {
		t.Error("falta asura como 'sin intentar'")
	}
	// Botones de acción para ambos
	if !strings.Contains(body, "/sources/mangadex/retry") || !strings.Contains(body, "/sources/mangadex/unlink") {
		t.Error("faltan formularios de mangadex (retry/unlink)")
	}
	if !strings.Contains(body, "/sources/asura/retry") {
		t.Error("falta form retry para asura")
	}
	// Asura sin fila NO debe tener form de unlink.
	if strings.Contains(body, "/sources/asura/unlink") {
		t.Error("asura sin fila no debería ofrecer unlink")
	}
}
