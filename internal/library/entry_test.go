package library

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.odin.do/kengru/mokuji/internal/session"
)

var mangaSeq int64

// insertMangaWithChapters crea manga + library_entry + fila en
// manga_sources con la source 'mangadex' en el status dado. El sourceID
// se deriva del seq para que sea único entre tests.
func insertMangaWithChapters(t *testing.T, db *sql.DB, userID int64, sourceStatus string) (entryID, mangaID int64) {
	t.Helper()
	mangaSeq++
	anilistID := "a-" + strconv.FormatInt(mangaSeq, 10)
	res, err := db.Exec(
		`INSERT INTO manga (source, source_id, title, cover_url, created_at, updated_at)
		 VALUES ('anilist', ?, 'Berserk', '', ?, ?)`,
		anilistID, time.Now().Unix(), time.Now().Unix(),
	)
	if err != nil {
		t.Fatal(err)
	}
	mangaID, _ = res.LastInsertId()

	mangadexID := "mdx-" + anilistID
	if _, err := db.Exec(
		`INSERT INTO manga_sources (manga_id, source, source_id, status) VALUES (?, 'mangadex', ?, ?)`,
		mangaID, mangadexID, sourceStatus,
	); err != nil {
		t.Fatal(err)
	}

	res, err = db.Exec(
		`INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at) VALUES (?, ?, 'reading', ?, ?)`,
		userID, mangaID, time.Now().Unix(), time.Now().Unix(),
	)
	if err != nil {
		t.Fatal(err)
	}
	entryID, _ = res.LastInsertId()
	return entryID, mangaID
}

func insertChapter(t *testing.T, db *sql.DB, mangaID int64, sourceID, number, lang, externalURL string, publishedAt time.Time) int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO chapter (manga_id, source, source_id, number, language, published_at, external_url, created_at)
		VALUES (?, 'mangadex', ?, ?, ?, ?, ?, 0)
	`, mangaID, sourceID, number, lang, publishedAt.Unix(), externalURL)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func authReq(method, target string, body string, userID int64) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r = session.WithUserID(r, userID)
	return r
}

func TestEntryDetailRequiresSession(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)

	r := httptest.NewRequest("GET", "/library/entry/1", nil)
	r.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status: %d", w.Code)
	}
}

func TestEntryDetail404ForOtherUserEntry(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	alice := insertUser(t, db, "alice@example.com")
	bob := insertUser(t, db, "bob@example.com")
	entryID, _ := insertMangaWithChapters(t, db, bob, "matched")

	r := authReq("GET", "/library/entry/"+strconv.FormatInt(entryID, 10), "", alice)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: %d", w.Code)
	}
}

// hasChapterForm devuelve true si el body HTML incluye un hidden input
// chapter_id apuntando al id dado. Más confiable que buscar source_id en
// el body (que no se renderea como tal en el template).
func hasChapterForm(body string, chapterID int64) bool {
	return strings.Contains(body, `name="chapter_id" value="`+strconv.FormatInt(chapterID, 10)+`"`)
}

func TestEntryDetailListsChaptersDescByDefault(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")

	chOld := insertChapter(t, db, mangaID, "ch-old", "1", "en", "https://mangadex.org/chapter/ch-old", time.Now().Add(-48*time.Hour))
	chNew := insertChapter(t, db, mangaID, "ch-new", "2", "en", "https://mangadex.org/chapter/ch-new", time.Now().Add(-1*time.Hour))

	r := authReq("GET", "/library/entry/"+strconv.FormatInt(entryID, 10), "", uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	newNeedle := `name="chapter_id" value="` + strconv.FormatInt(chNew, 10) + `"`
	oldNeedle := `name="chapter_id" value="` + strconv.FormatInt(chOld, 10) + `"`
	newIdx := strings.Index(body, newNeedle)
	oldIdx := strings.Index(body, oldNeedle)
	if newIdx == -1 || oldIdx == -1 {
		t.Fatalf("falta alguno de los caps en body (new=%d old=%d)", newIdx, oldIdx)
	}
	if newIdx > oldIdx {
		t.Errorf("orden default no es desc: new aparece después de old")
	}
}

// Multi-source: cuando hay varias filas chapter con el mismo número
// (típico de Comick con múltiples scanlators), entry detail debe
// mostrar UNA fila por número con TODAS las opciones de lectura como
// botones "leer en X".
func TestEntryDetailGroupsByNumberAndShowsAllOptions(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")

	// Tres filas del MISMO cap (número "70") desde tres scanlators distintos.
	// Las fechas distintas verifican que el orden interno de ReadOptions es
	// "más reciente primero" (la URL del row más nuevo es la primaria).
	// group_name lleva el nombre real del scanlator — el chapter.title
	// queda vacío (Comick usa title para el título narrativo del cap, no
	// el scanlator).
	now := time.Now()
	mangakak := insertChapterWithGroup(t, db, mangaID, "comick", "ch-cm-70", "70", "Zaza", "Mangakakalot", "en", "https://comick.art/x/70-mangakak", now.Add(-1*time.Hour))
	insertChapterWithGroup(t, db, mangaID, "comick", "ch-cm-70-zaza", "70", "Zaza", "Zaza Scans", "en", "https://comick.art/x/70-zaza", now.Add(-24*time.Hour))
	insertChapterWithGroup(t, db, mangaID, "asura", "ch-asu-70", "70", "", "", "en", "https://asurascans.com/c/x/chapter/70", now.Add(-2*time.Hour))

	r := authReq("GET", "/library/entry/"+strconv.FormatInt(entryID, 10), "", uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// Debe haber UNA mención de "cap. 70" (no tres como sin dedup).
	if got := strings.Count(body, ">cap. 70<"); got != 1 {
		t.Errorf("debería renderear un solo header 'cap. 70', obtuvo %d", got)
	}

	// Cada ReadOption renderea un form POST /read con su chapter_id —
	// el handler hace el redirect al external_url, así que la URL no
	// aparece directo en el HTML. Verificamos que estén los 3 botones.
	if got := strings.Count(body, "/library/entry/"+strconv.FormatInt(entryID, 10)+"/read"); got != 3 {
		t.Errorf("debería haber 3 forms de /read (uno por scanlator), obtuvo %d", got)
	}

	// Botones de leer toman el label de group_name (real scanlator), no
	// del chapter.title. Asura no llena group_name → fallback al source name.
	for _, label := range []string{"leer en Mangakakalot", "leer en Zaza Scans", "leer en asura"} {
		if !strings.Contains(body, label) {
			t.Errorf("falta botón %q", label)
		}
	}
	// El chapter.title ("Zaza") aparece en el HEADER, no en los botones.
	if !strings.Contains(body, "chapter-title\">Zaza<") {
		t.Errorf("chapter title 'Zaza' debería estar en el header del grupo")
	}

	// El cap_id del más reciente (mangakak) debe ser la opción primaria —
	// significa que cuando user clickea "marcar leído", apunta a ese row.
	wantPrimary := `name="chapter_id" value="` + strconv.FormatInt(mangakak, 10) + `"`
	if !strings.Contains(body, wantPrimary) {
		t.Errorf("primary chapter_id (mangakak=%d) no aparece en mark-read form", mangakak)
	}
}

// insertChapterWithGroup es una variante de insertChapter que permite
// especificar source + title + group_name (útil para los tests
// multi-source de agrupamiento, donde necesitamos distinguir el
// scanlator del título de la historia).
func insertChapterWithGroup(t *testing.T, db *sql.DB, mangaID int64, source, sourceID, number, title, group, lang, externalURL string, publishedAt time.Time) int64 {
	t.Helper()
	var titleArg, groupArg any
	if title != "" {
		titleArg = title
	}
	if group != "" {
		groupArg = group
	}
	res, err := db.Exec(`
		INSERT INTO chapter (manga_id, source, source_id, number, title, language, published_at, external_url, group_name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
	`, mangaID, source, sourceID, number, titleArg, lang, publishedAt.Unix(), externalURL, groupArg)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestEntryDetailSortAsc(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	chOld := insertChapter(t, db, mangaID, "ch-old", "1", "en", "", time.Now().Add(-48*time.Hour))
	chNew := insertChapter(t, db, mangaID, "ch-new", "2", "en", "", time.Now().Add(-1*time.Hour))

	r := authReq("GET", "/library/entry/"+strconv.FormatInt(entryID, 10)+"?sort=asc", "", uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	body := w.Body.String()
	oldNeedle := `name="chapter_id" value="` + strconv.FormatInt(chOld, 10) + `"`
	newNeedle := `name="chapter_id" value="` + strconv.FormatInt(chNew, 10) + `"`
	if strings.Index(body, oldNeedle) > strings.Index(body, newNeedle) {
		t.Error("sort=asc no aplicado")
	}
}

func TestEntryDetailHidesReadByDefault(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	chRead := insertChapter(t, db, mangaID, "ch-read", "1", "en", "", time.Now().Add(-48*time.Hour))
	chNew := insertChapter(t, db, mangaID, "ch-new", "2", "en", "", time.Now().Add(-1*time.Hour))

	// Set cursor to the number of chRead via subquery — el cursor por
	// número evita acoplar el test a IDs específicos.
	if _, err := db.Exec(`UPDATE library_entries
		SET last_read_chapter_number = (SELECT CAST(number AS REAL) FROM chapter WHERE id = ?)
		WHERE id = ?`, chRead, entryID); err != nil {
		t.Fatal(err)
	}

	r := authReq("GET", "/library/entry/"+strconv.FormatInt(entryID, 10), "", uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	body := w.Body.String()
	if hasChapterForm(body, chRead) {
		t.Error("cap leído visible sin include_read=1")
	}
	if !hasChapterForm(body, chNew) {
		t.Error("cap no leído ausente")
	}
}

func TestEntryDetailIncludeReadShowsAll(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	chRead := insertChapter(t, db, mangaID, "ch-read", "1", "en", "", time.Now().Add(-48*time.Hour))
	chNew := insertChapter(t, db, mangaID, "ch-new", "2", "en", "", time.Now().Add(-1*time.Hour))
	// Set cursor to the number of chRead via subquery — el cursor por
	// número evita acoplar el test a IDs específicos.
	if _, err := db.Exec(`UPDATE library_entries
		SET last_read_chapter_number = (SELECT CAST(number AS REAL) FROM chapter WHERE id = ?)
		WHERE id = ?`, chRead, entryID); err != nil {
		t.Fatal(err)
	}

	r := authReq("GET", "/library/entry/"+strconv.FormatInt(entryID, 10)+"?include_read=1", "", uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	body := w.Body.String()
	if !hasChapterForm(body, chRead) {
		t.Error("include_read=1 no muestra leídos")
	}
	if !hasChapterForm(body, chNew) {
		t.Error("include_read=1 oculta no leídos")
	}
}

func TestEntryReadAndOpenAdvancesCursorAndRendersRedirectPage(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	chID := insertChapter(t, db, mangaID, "ch-1", "1", "en", "https://reader.example/ch-1", time.Now().Add(-1*time.Hour))

	form := url.Values{"chapter_id": {strconv.FormatInt(chID, 10)}}
	r := authReq("POST", "/library/entry/"+strconv.FormatInt(entryID, 10)+"/read", form.Encode(), uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryReadAndOpen(w, r)

	// La respuesta es HTML con meta refresh (no 303) para evitar CSP
	// form-action bloqueando redirects cross-origin del form submission.
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `meta http-equiv="refresh"`) {
		t.Errorf("falta meta refresh en body: %s", body)
	}
	if !strings.Contains(body, "https://reader.example/ch-1") {
		t.Errorf("URL ausente en body: %s", body)
	}

	// Cursor por número: el chID tiene number="1", el cursor debe quedar en 1.0.
	var cur sql.NullFloat64
	if err := db.QueryRow(`SELECT last_read_chapter_number FROM library_entries WHERE id = ?`, entryID).Scan(&cur); err != nil {
		t.Fatal(err)
	}
	if !cur.Valid || cur.Float64 != 1.0 {
		t.Errorf("cursor: esperado 1.0 (number del cap), obtenido %v", cur)
	}
}

func TestEntryReadAndOpenFallsBackToMangadexURL(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	chID := insertChapter(t, db, mangaID, "ch-x", "1", "en", "", time.Now().Add(-1*time.Hour))

	form := url.Values{"chapter_id": {strconv.FormatInt(chID, 10)}}
	r := authReq("POST", "/library/entry/"+strconv.FormatInt(entryID, 10)+"/read", form.Encode(), uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryReadAndOpen(w, r)

	if !strings.Contains(w.Body.String(), "https://mangadex.org/chapter/ch-x") {
		t.Errorf("fallback URL ausente en body: %s", w.Body.String())
	}
}

func TestEntryReadAndOpen404OnOtherUserChapter(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	alice := insertUser(t, db, "alice@example.com")
	bob := insertUser(t, db, "bob@example.com")
	aliceEntry, _ := insertMangaWithChapters(t, db, alice, "matched")
	_, bobMangaID := insertMangaWithChapters(t, db, bob, "matched")
	bobChID := insertChapter(t, db, bobMangaID, "ch-bob", "1", "en", "", time.Now().Add(-1*time.Hour))

	// Alice intenta marcar leído un cap de Bob, pasando su propio entry id.
	form := url.Values{"chapter_id": {strconv.FormatInt(bobChID, 10)}}
	r := authReq("POST", "/library/entry/"+strconv.FormatInt(aliceEntry, 10)+"/read", form.Encode(), alice)
	r.SetPathValue("id", strconv.FormatInt(aliceEntry, 10))
	w := httptest.NewRecorder()
	h.entryReadAndOpen(w, r)

	// markChapterRead devuelve error (mismatch entry/chapter) → 500.
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status: %d (esperaba 500 por mismatch)", w.Code)
	}

	// El cursor de Alice no debe haberse tocado.
	var cur sql.NullFloat64
	_ = db.QueryRow(`SELECT last_read_chapter_number FROM library_entries WHERE id = ?`, aliceEntry).Scan(&cur)
	if cur.Valid {
		t.Error("cursor de Alice fue alterado por chapter de Bob")
	}
}

func TestEntryMarkReadRedirectsInternal(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	chID := insertChapter(t, db, mangaID, "ch-1", "1", "en", "https://reader.example/ch-1", time.Now().Add(-1*time.Hour))

	form := url.Values{"chapter_id": {strconv.FormatInt(chID, 10)}}
	r := authReq("POST", "/library/entry/"+strconv.FormatInt(entryID, 10)+"/mark-read", form.Encode(), uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryMarkRead(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status: %d", w.Code)
	}
	want := "/library/entry/" + strconv.FormatInt(entryID, 10)
	if loc := w.Header().Get("Location"); loc != want {
		t.Errorf("Location: %q, want %q", loc, want)
	}
}

func TestEntryCursorAllowsArbitraryValue(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, _ := insertMangaWithChapters(t, db, uid, "matched")
	// Setup: cursor empieza en 231 (caso bug del user que marcó tarde).
	if _, err := db.Exec(`UPDATE library_entries SET last_read_chapter_number = 231 WHERE id = ?`, entryID); err != nil {
		t.Fatalf("setup cursor: %v", err)
	}

	form := url.Values{"last_read_chapter_number": {"1"}}
	r := authReq("POST", "/library/entry/"+strconv.FormatInt(entryID, 10)+"/cursor", form.Encode(), uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryCursor(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d, want 303. body: %s", w.Code, w.Body.String())
	}
	want := "/library/entry/" + strconv.FormatInt(entryID, 10)
	if loc := w.Header().Get("Location"); loc != want {
		t.Errorf("Location: %q, want %q", loc, want)
	}
	var cursor float64
	if err := db.QueryRow(`SELECT last_read_chapter_number FROM library_entries WHERE id = ?`, entryID).Scan(&cursor); err != nil {
		t.Fatalf("scan cursor: %v", err)
	}
	if cursor != 1.0 {
		t.Errorf("cursor: got %v, want 1.0 (retroceso explícito permitido)", cursor)
	}
}

func TestEntryCursorRejectsNegative(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, _ := insertMangaWithChapters(t, db, uid, "matched")

	form := url.Values{"last_read_chapter_number": {"-3"}}
	r := authReq("POST", "/library/entry/"+strconv.FormatInt(entryID, 10)+"/cursor", form.Encode(), uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryCursor(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}

func TestEntryDetailPaginatesChapters(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	// Insertar 250 caps en inglés (=preferred_language default).
	for i := 1; i <= 250; i++ {
		insertChapter(t, db, mangaID,
			fmt.Sprintf("ch-%d", i),
			strconv.Itoa(i),
			"en",
			fmt.Sprintf("https://reader.example/%d", i),
			time.Now().Add(-time.Duration(i)*time.Hour))
	}

	r := authReq("GET", "/library/entry/"+strconv.FormatInt(entryID, 10)+"?page=1", "", uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// Cuenta la string que entry.html usa para identificar un cap row.
	count := strings.Count(body, `class="chapter-num"`)
	if count != EntryChaptersPageSize {
		t.Errorf("page 1: %d caps renderizados, want %d", count, EntryChaptersPageSize)
	}
	// La UI link a page=2 se renderiza en Task 4. En este test (Task 3)
	// validamos que el handler reciba page=1 correctamente y renderice
	// exactamente 100 caps. La existencia del link viene después.
}

func TestEntryDetailPageTwoReturnsNextSlice(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	for i := 1; i <= 250; i++ {
		insertChapter(t, db, mangaID,
			fmt.Sprintf("ch-%d", i),
			strconv.Itoa(i),
			"en",
			fmt.Sprintf("https://reader.example/%d", i),
			time.Now().Add(-time.Duration(i)*time.Hour))
	}

	r := authReq("GET", "/library/entry/"+strconv.FormatInt(entryID, 10)+"?page=2", "", uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	body := w.Body.String()
	count := strings.Count(body, `class="chapter-num"`)
	if count != EntryChaptersPageSize {
		t.Errorf("page 2: %d caps renderizados, want %d", count, EntryChaptersPageSize)
	}
}

func TestEntryDetailPageOutOfBoundsClampsToLast(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	// Solo 10 caps — una sola página posible.
	for i := 1; i <= 10; i++ {
		insertChapter(t, db, mangaID,
			fmt.Sprintf("ch-%d", i),
			strconv.Itoa(i),
			"en",
			fmt.Sprintf("https://reader.example/%d", i),
			time.Now().Add(-time.Duration(i)*time.Hour))
	}

	// Pedir page=999 — el handler clampa a la última página válida (1) y
	// renderiza normal. No 500, no crash.
	r := authReq("GET", "/library/entry/"+strconv.FormatInt(entryID, 10)+"?page=999", "", uid)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryDetail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d (out-of-bounds debe devolver 200)", w.Code)
	}
	body := w.Body.String()
	count := strings.Count(body, `class="chapter-num"`)
	if count != 10 {
		t.Errorf("page 999 (clamp to 1): %d caps, want 10", count)
	}
}

func TestEntryCursorScopedToOwner(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	owner := insertUser(t, db, "alice@example.com")
	intruder := insertUser(t, db, "bob@example.com")
	entryID, _ := insertMangaWithChapters(t, db, owner, "matched")
	if _, err := db.Exec(`UPDATE library_entries SET last_read_chapter_number = 5 WHERE id = ?`, entryID); err != nil {
		t.Fatalf("setup cursor: %v", err)
	}

	form := url.Values{"last_read_chapter_number": {"100"}}
	r := authReq("POST", "/library/entry/"+strconv.FormatInt(entryID, 10)+"/cursor", form.Encode(), intruder)
	r.SetPathValue("id", strconv.FormatInt(entryID, 10))
	w := httptest.NewRecorder()
	h.entryCursor(w, r)

	// 404 sin revelar la existencia del entry del owner.
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", w.Code)
	}
	var cursor float64
	if err := db.QueryRow(`SELECT last_read_chapter_number FROM library_entries WHERE id = ?`, entryID).Scan(&cursor); err != nil {
		t.Fatalf("scan cursor: %v", err)
	}
	if cursor != 5.0 {
		t.Errorf("cursor del owner cambió: got %v, want 5.0", cursor)
	}
}
