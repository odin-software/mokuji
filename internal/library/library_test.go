package library

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/odin-software/mokuji/internal/mangadex"
	"github.com/odin-software/mokuji/internal/mangasource"
	"github.com/odin-software/mokuji/internal/migrations"
	"github.com/odin-software/mokuji/internal/releases/matcher"
	"github.com/odin-software/mokuji/internal/session"
)

// fakeSource es una mangasource.Source para tests.
type fakeSource struct {
	results []mangasource.SearchResult
	err     error
	calls   int
}

func (f *fakeSource) Name() string { return "fake" }
func (f *fakeSource) Search(ctx context.Context, query string) ([]mangasource.SearchResult, error) {
	f.calls++
	return f.results, f.err
}

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	src := os.DirFS("../../migrations")
	if _, err := migrations.Run(db, src); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func insertUser(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO users (email, created_at) VALUES (?, ?)`, email, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func newTestHandler(t *testing.T, db *sql.DB, src mangasource.Source) *Handler {
	t.Helper()
	if src == nil {
		src = &fakeSource{}
	}
	return &Handler{
		db:     db,
		src:    src,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func newFullHandler(t *testing.T, db *sql.DB, src mangasource.Source) *Handler {
	t.Helper()
	if src == nil {
		src = &fakeSource{}
	}
	h, err := New(Config{
		DB:            db,
		MangaSource:   src,
		HTMLTemplates: os.DirFS("../../web/templates"),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestUpsertManga(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	id1, err := h.upsertManga(ctx, "anilist", "30002", "Berserk", "cover.jpg")
	if err != nil {
		t.Fatalf("upsert 1: %v", err)
	}

	// Mismo source+source_id: mismo ID, metadata actualizada.
	id2, err := h.upsertManga(ctx, "anilist", "30002", "Berserk (updated)", "cover2.jpg")
	if err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	if id1 != id2 {
		t.Errorf("upsert con mismo source_id retornó ID distinto: %d vs %d", id1, id2)
	}

	var title, coverURL string
	if err := db.QueryRow(`SELECT title, cover_url FROM manga WHERE id = ?`, id1).Scan(&title, &coverURL); err != nil {
		t.Fatal(err)
	}
	if title != "Berserk (updated)" {
		t.Errorf("title no actualizado: %q", title)
	}
	if coverURL != "cover2.jpg" {
		t.Errorf("cover no actualizado: %q", coverURL)
	}
}

func TestCreateEntryAndList(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	userID := insertUser(t, db, "alice@example.com")
	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "Berserk", "cover.jpg")

	if err := h.createEntry(ctx, userID, mangaID); err != nil {
		t.Fatalf("createEntry: %v", err)
	}

	entries, err := h.listEntries(ctx, userID, "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("esperado 1 entry, obtenido %d", len(entries))
	}
	e := entries[0]
	if e.Manga.Title != "Berserk" {
		t.Errorf("title: %q", e.Manga.Title)
	}
	if e.Status != "reading" {
		t.Errorf("status default: %q", e.Status)
	}
}

func TestDuplicateEntryRejected(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	userID := insertUser(t, db, "alice@example.com")
	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "Berserk", "")

	if err := h.createEntry(ctx, userID, mangaID); err != nil {
		t.Fatal(err)
	}

	err := h.createEntry(ctx, userID, mangaID)
	if err == nil {
		t.Fatal("se esperaba error por UNIQUE constraint")
	}
	if !isUniqueConstraintError(err) {
		t.Errorf("isUniqueConstraintError no detectó: %v", err)
	}
}

func TestListEntriesFilterByStatus(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	userID := insertUser(t, db, "alice@example.com")
	for i, status := range []string{"reading", "planned", "completed"} {
		mid, _ := h.upsertManga(ctx, "anilist", strconv.Itoa(i+1), "Manga "+status, "")
		_, err := db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			userID, mid, status, time.Now().Unix(), time.Now().Unix())
		if err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		filter string
		want   int
	}{
		{"all", 3},
		{"reading", 1},
		{"planned", 1},
		{"completed", 1},
	}
	for _, tc := range cases {
		entries, err := h.listEntries(ctx, userID, tc.filter, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != tc.want {
			t.Errorf("filter=%q: esperado %d, obtenido %d", tc.filter, tc.want, len(entries))
		}
	}
}

func TestListEntriesSearchInLibrary(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	userID := insertUser(t, db, "alice@example.com")
	for i, title := range []string{"Berserk", "Vagabond", "Chainsaw Man"} {
		mid, _ := h.upsertManga(ctx, "anilist", strconv.Itoa(i+1), title, "")
		_, err := db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at) VALUES (?, ?, 'reading', ?, ?)`,
			userID, mid, time.Now().Unix(), time.Now().Unix())
		if err != nil {
			t.Fatal(err)
		}
	}

	entries, err := h.listEntries(ctx, userID, "all", "Chain")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Manga.Title != "Chainsaw Man" {
		t.Errorf("búsqueda 'Chain': %v", entries)
	}
}

func TestListEntriesOrderByStatus(t *testing.T) {
	// El orden default es: reading > planned > completed (con favorito
	// como tie-break dentro del bucket). Insertamos uno de cada estado en
	// orden cronológico inverso para probar que el sort no cae al
	// updated_at solo.
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	userID := insertUser(t, db, "alice@example.com")
	now := time.Now().Unix()

	cases := []struct {
		title  string
		status string
	}{
		{"C-completed", "completed"},
		{"B-planned", "planned"},
		{"A-reading", "reading"},
	}
	for i, c := range cases {
		mid, _ := h.upsertManga(ctx, "anilist", strconv.Itoa(i+1), c.title, "")
		_, err := db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			userID, mid, c.status, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}

	entries, err := h.listEntries(ctx, userID, "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("esperaba 3 entries, obtuve %d", len(entries))
	}
	want := []string{"A-reading", "B-planned", "C-completed"}
	for i, w := range want {
		if entries[i].Manga.Title != w {
			t.Errorf("posición %d: esperaba %q, obtuve %q", i, w, entries[i].Manga.Title)
		}
	}
}

func TestListEntriesFavoriteFirst(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	userID := insertUser(t, db, "alice@example.com")
	now := time.Now().Unix()

	// agrega 3 entries; el segundo es favorito. updated_at uniforme, así
	// solo is_favorite decide el orden.
	for i, title := range []string{"A", "B-fav", "C"} {
		mid, _ := h.upsertManga(ctx, "anilist", strconv.Itoa(i+1), title, "")
		fav := 0
		if i == 1 {
			fav = 1
		}
		_, err := db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, is_favorite, created_at, updated_at) VALUES (?, ?, 'reading', ?, ?, ?)`,
			userID, mid, fav, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}

	entries, err := h.listEntries(ctx, userID, "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Manga.Title != "B-fav" {
		t.Errorf("favorito no quedó primero: %+v", entries)
	}
}

func TestDeleteRejectsOtherUsersEntry(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	alice := insertUser(t, db, "alice@example.com")
	bob := insertUser(t, db, "bob@example.com")

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "Berserk", "")
	res, _ := db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at) VALUES (?, ?, 'reading', ?, ?)`,
		alice, mangaID, time.Now().Unix(), time.Now().Unix())
	aliceEntryID, _ := res.LastInsertId()

	req := httptest.NewRequest(http.MethodPost,
		"/library/"+strconv.FormatInt(aliceEntryID, 10)+"/delete", nil)
	req.SetPathValue("id", strconv.FormatInt(aliceEntryID, 10))
	req = session.WithUserID(req, bob)

	rec := httptest.NewRecorder()
	h.delete(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: esperado 404, obtenido %d", rec.Code)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM library_entries WHERE id = ?`, aliceEntryID).Scan(&count)
	if count != 1 {
		t.Error("entry de alice fue borrada por bob")
	}
}

func TestListWithQueryRendersCatalogAndDedup(t *testing.T) {
	db := setupDB(t)
	src := &fakeSource{
		results: []mangasource.SearchResult{
			{SourceID: "1", Title: "Berserk", CoverURL: "https://cdn/b.jpg", Description: "Dark fantasy"},
			{SourceID: "2", Title: "Vagabond", CoverURL: "https://cdn/v.jpg"},
		},
	}
	h := newFullHandler(t, db, src)
	userID := insertUser(t, db, "alice@example.com")

	// El user ya tiene Berserk (source_id 1) en su biblioteca.
	ctx := context.Background()
	mangaID, _ := h.upsertManga(ctx, src.Name(), "1", "Berserk", "")
	if err := h.createEntry(ctx, userID, mangaID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/library?q=ber", nil)
	req = req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID, CSRFToken: "tok123"}))
	rec := httptest.NewRecorder()
	h.list(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", rec.Code)
	}
	if src.calls != 1 {
		t.Errorf("esperaba 1 call a la source, hubo %d", src.calls)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "agregar nuevo") {
		t.Error("falta el header de la sección de catálogo")
	}
	if !strings.Contains(body, "Vagabond") {
		t.Error("falta el resultado de catálogo no-poseído (Vagabond)")
	}
	if !strings.Contains(body, "ya en tu biblioteca") {
		t.Error("Berserk (ya poseído) debería marcarse 'ya en tu biblioteca'")
	}
}

func TestListCatalogErrorStillRendersLibrary(t *testing.T) {
	db := setupDB(t)
	src := &fakeSource{err: errors.New("anilist caído")}
	h := newFullHandler(t, db, src)
	userID := insertUser(t, db, "alice@example.com")

	ctx := context.Background()
	mangaID, _ := h.upsertManga(ctx, src.Name(), "1", "Berserk", "")
	if err := h.createEntry(ctx, userID, mangaID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/library?q=ber", nil)
	req = req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID, CSRFToken: "tok"}))
	rec := httptest.NewRecorder()
	h.list(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Berserk") {
		t.Error("la lista de biblioteca debe renderizar pese al error de catálogo")
	}
	if !strings.Contains(body, "no pudimos buscar en el catálogo") {
		t.Error("falta el mensaje de CatalogError cuando la búsqueda externa falla")
	}
}

func TestListEmptyQueryNoCatalogCall(t *testing.T) {
	db := setupDB(t)
	src := &fakeSource{}
	h := newFullHandler(t, db, src)
	userID := insertUser(t, db, "alice@example.com")

	req := httptest.NewRequest(http.MethodGet, "/library", nil)
	req = req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID}))
	rec := httptest.NewRecorder()
	h.list(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", rec.Code)
	}
	if src.calls != 0 {
		t.Errorf("query vacía no debe llamar a la source; hubo %d calls", src.calls)
	}
}

func TestSearchRedirectsToLibraryWithQuery(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	userID := insertUser(t, db, "alice@example.com")

	req := httptest.NewRequest(http.MethodGet, "/library/search?q=berserk", nil)
	req = req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID}))
	rec := httptest.NewRecorder()
	h.search(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: esperado 303, obtenido %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/library?q=berserk" {
		t.Errorf("Location: esperado /library?q=berserk, obtenido %q", loc)
	}
}

func postAdd(h *Handler, userID int64, sourceID string) *httptest.ResponseRecorder {
	form := "source=anilist&source_id=" + sourceID + "&title=Berserk"
	req := httptest.NewRequest(http.MethodPost, "/library/add", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID}))
	rec := httptest.NewRecorder()
	h.add(rec, req)
	return rec
}

func TestAddRedirectsToEntryDetail(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	userID := insertUser(t, db, "alice@example.com")

	rec := postAdd(h, userID, "30002")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: esperado 303, obtenido %d", rec.Code)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM library_entries WHERE user_id = ?`, userID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if loc := rec.Header().Get("Location"); loc != "/library/entry/"+strconv.FormatInt(id, 10) {
		t.Errorf("Location: esperado el detalle de la entry %d, obtenido %q", id, loc)
	}
}

func TestAddExistingRedirectsToExistingEntry(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	userID := insertUser(t, db, "alice@example.com")

	first := postAdd(h, userID, "30002")
	second := postAdd(h, userID, "30002")
	if second.Code != http.StatusSeeOther {
		t.Fatalf("segundo add: esperado 303, obtenido %d", second.Code)
	}
	if a, b := first.Header().Get("Location"), second.Header().Get("Location"); a != b || !strings.HasPrefix(a, "/library/entry/") {
		t.Errorf("Location: primero %q, segundo %q; esperado el mismo detalle", a, b)
	}
}

func TestListAnonymousRedirects(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)

	req := httptest.NewRequest(http.MethodGet, "/library", nil)
	rec := httptest.NewRecorder()
	h.list(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: esperado 303, obtenido %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/auth" {
		t.Errorf("location: esperado /auth, obtenido %q", loc)
	}
}

// --- Matching contra una source al agregar manga ---

// fakeMatcher implementa la interface Matcher para tests del flow add.
type fakeMatcher struct {
	result matcher.Result
	err    error
	calls  []string
}

func (f *fakeMatcher) Match(_ context.Context, titles ...string) (matcher.Result, error) {
	f.calls = append(f.calls, titles...)
	return f.result, f.err
}

// withMatcher reemplaza el map de matchers con uno solo bajo el nombre
// "mangadex" (la única source en este commit). Centraliza el setup para
// no repetir map literals.
func withMatcher(h *Handler, m Matcher) {
	h.matchers = map[string]Matcher{"mangadex": m}
}

// sourceStatusForManga lee status + source_id de la fila mangadex en
// manga_sources. NoRows se reporta como ("unmatched", invalid) — el caller
// puede distinguir "fila inexistente" vs "fila con status x".
func sourceStatusForManga(t *testing.T, db *sql.DB, mangaID int64) (status string, id sql.NullString) {
	t.Helper()
	err := db.QueryRow(`
		SELECT status, NULLIF(source_id, '') FROM manga_sources
		WHERE manga_id = ? AND source = 'mangadex'
	`, mangaID).Scan(&status, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return "unmatched", sql.NullString{}
	}
	if err != nil {
		t.Fatalf("query manga_sources: %v", err)
	}
	return
}

func TestTryMatchMangaPersistsMatched(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	withMatcher(h, &fakeMatcher{result: matcher.Result{
		Status:   matcher.StatusMatched,
		SourceID: "md-uuid",
	}})
	ctx := context.Background()

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "Chainsaw Man", "")
	h.tryMatchSources(ctx, mangaID, "Chainsaw Man")

	status, id := sourceStatusForManga(t, db, mangaID)
	if status != "matched" {
		t.Errorf("status: esperado matched, obtenido %q", status)
	}
	if !id.Valid || id.String != "md-uuid" {
		t.Errorf("source_id: esperado 'md-uuid', obtenido %+v", id)
	}
}

func TestTryMatchMangaPersistsNeedsReviewWithoutID(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	withMatcher(h, &fakeMatcher{result: matcher.Result{
		Status: matcher.StatusNeedsReview,
		Candidates: []matcher.Candidate{
			{ID: "a"}, {ID: "b"},
		},
	}})
	ctx := context.Background()

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "Berserk", "")
	h.tryMatchSources(ctx, mangaID, "Berserk")

	status, id := sourceStatusForManga(t, db, mangaID)
	if status != "needs_review" {
		t.Errorf("status: esperado needs_review, obtenido %q", status)
	}
	if id.Valid {
		t.Errorf("source_id no debería estar poblado para needs_review, obtenido %q", id.String)
	}
}

func TestTryMatchMangaPersistsNoResults(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	withMatcher(h, &fakeMatcher{result: matcher.Result{Status: matcher.StatusNoResults}})
	ctx := context.Background()

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "Title Sin Match", "")
	h.tryMatchSources(ctx, mangaID, "Title Sin Match")

	status, id := sourceStatusForManga(t, db, mangaID)
	if status != "no_results" {
		t.Errorf("status: esperado no_results, obtenido %q", status)
	}
	if id.Valid {
		t.Error("source_id no debería estar poblado para no_results")
	}
}

func TestTryMatchMangaLeavesUnmatchedOnMatcherError(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	withMatcher(h, &fakeMatcher{err: errors.New("network down")})
	ctx := context.Background()

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "Bleach", "")
	h.tryMatchSources(ctx, mangaID, "Bleach")

	status, id := sourceStatusForManga(t, db, mangaID)
	if status != "unmatched" {
		t.Errorf("status: esperado unmatched (sin fila persistida), obtenido %q", status)
	}
	if id.Valid {
		t.Error("source_id no debería estar poblado tras error de matcher")
	}
}

func TestTryMatchMangaLeavesUnmatchedOnRateLimited(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	withMatcher(h, &fakeMatcher{err: mangadex.ErrRateLimited})
	ctx := context.Background()

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "Naruto", "")
	h.tryMatchSources(ctx, mangaID, "Naruto")

	status, _ := sourceStatusForManga(t, db, mangaID)
	if status != "unmatched" {
		t.Errorf("status: esperado unmatched tras rate limit (worker reintenta luego), obtenido %q", status)
	}
}

func TestTryMatchMangaIsNoOpWhenMatcherNotConfigured(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil) // matchers nil
	ctx := context.Background()

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "Vinland Saga", "")
	h.tryMatchSources(ctx, mangaID, "Vinland Saga")

	status, _ := sourceStatusForManga(t, db, mangaID)
	if status != "unmatched" {
		t.Errorf("sin matchers cableados, debe quedar unmatched, obtenido %q", status)
	}
}

// fakePoller registra cada llamada para que los tests verifiquen el trigger.
// PollOne corre en goroutine fire-and-forget desde el handler, así que el
// poller usa un canal + WaitGroup para que los tests puedan esperar el
// resultado deterministicamente (sin sleep).
type fakePoller struct {
	mu    sync.Mutex
	calls []fakePollerCall
	done  chan struct{} // cerrado tras cada PollOne; tests recibe para esperar
	err   error
}

type fakePollerCall struct {
	mangaID  int64
	source   string
	sourceID string
}

func newFakePoller() *fakePoller {
	return &fakePoller{done: make(chan struct{}, 4)}
}

func (f *fakePoller) PollOne(_ context.Context, mangaID int64, source, sourceID string) error {
	f.mu.Lock()
	f.calls = append(f.calls, fakePollerCall{mangaID: mangaID, source: source, sourceID: sourceID})
	f.mu.Unlock()
	select {
	case f.done <- struct{}{}:
	default:
	}
	return f.err
}

// waitForCall espera hasta que el poller registre al menos N calls, o
// timeout. Los tests sincronizan así contra la goroutine que dispara
// PollOne.
func (f *fakePoller) waitForCall(t *testing.T, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := len(f.calls)
		f.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-f.done:
		case <-time.After(10 * time.Millisecond):
		}
	}
	f.mu.Lock()
	got := len(f.calls)
	f.mu.Unlock()
	t.Fatalf("timeout esperando %d calls al poller; tras %v hay %d", want, timeout, got)
}

func (f *fakePoller) callsSnapshot() []fakePollerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakePollerCall(nil), f.calls...)
}

func TestTryMatchMangaTriggersPollerOnMatched(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	withMatcher(h, &fakeMatcher{result: matcher.Result{
		Status:   matcher.StatusMatched,
		SourceID: "mdx-chainsaw",
	}})
	poller := newFakePoller()
	h.poller = poller

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "Chainsaw Man", "")
	h.tryMatchSources(ctx, mangaID, "Chainsaw Man")

	poller.waitForCall(t, 1, time.Second)
	calls := poller.callsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("PollOne calls: %d", len(calls))
	}
	call := calls[0]
	if call.mangaID != mangaID || call.source != "mangadex" || call.sourceID != "mdx-chainsaw" {
		t.Errorf("call inesperada: %+v", call)
	}
}

func TestTryMatchMangaDoesNotPollOnNeedsReview(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	withMatcher(h, &fakeMatcher{result: matcher.Result{Status: matcher.StatusNeedsReview}})
	poller := newFakePoller()
	h.poller = poller

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "Ambiguous", "")
	h.tryMatchSources(ctx, mangaID, "Ambiguous")

	// Dar ventana corta para que cualquier goroutine errónea aparezca.
	// PollOne corre fire-and-forget; needs_review no debería dispararla.
	time.Sleep(50 * time.Millisecond)
	if calls := poller.callsSnapshot(); len(calls) != 0 {
		t.Errorf("PollOne fue llamado para needs_review: %d veces", len(calls))
	}
}

func TestTryMatchMangaDoesNotPollWhenNotConfigured(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	withMatcher(h, &fakeMatcher{result: matcher.Result{
		Status:   matcher.StatusMatched,
		SourceID: "mdx-x",
	}})
	// h.poller queda nil — debe ser no-op.

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "X", "")
	h.tryMatchSources(ctx, mangaID, "X")
	// Sin poller no hay nada que assertar más allá de que el match
	// persiste sin panicar.
	status, _ := sourceStatusForManga(t, db, mangaID)
	if status != "matched" {
		t.Errorf("status: %q", status)
	}
}

func TestTryMatchMangaSwallowsPollerError(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	withMatcher(h, &fakeMatcher{result: matcher.Result{
		Status:   matcher.StatusMatched,
		SourceID: "mdx-x",
	}})
	poller := newFakePoller()
	poller.err = errors.New("mangadex caído")
	h.poller = poller

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "X", "")
	// No debe panicar ni propagar — el match persiste y el worker
	// retoma el manga en el próximo tick (next_check_at sigue NULL).
	h.tryMatchSources(ctx, mangaID, "X")
	// Esperar a que la goroutine de PollOne corra y absorba el error.
	poller.waitForCall(t, 1, time.Second)

	status, _ := sourceStatusForManga(t, db, mangaID)
	if status != "matched" {
		t.Errorf("status: %q (esperaba matched aunque el poller falle)", status)
	}
}

func TestUpsertMangaSourceWithEmptyID(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "X", "")
	if err := h.upsertMangaSource(ctx, mangaID, "mangadex", "no_results", ""); err != nil {
		t.Fatal(err)
	}
	status, id := sourceStatusForManga(t, db, mangaID)
	if status != "no_results" {
		t.Errorf("status: %q", status)
	}
	if id.Valid {
		t.Errorf("source_id debería ser NULL (vacío) para no_results, obtenido %q", id.String)
	}
}

func TestListEntriesCountsTotalAndUnread(t *testing.T) {
	// Verifica que el query de listEntries calcula bien los counts que
	// alimentan el badge: TotalChapters (todos los caps en idioma del user)
	// vs UnreadCount (caps posteriores al cursor).
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()
	userID := insertUser(t, db, "alice@example.com")

	mangaID, _ := h.upsertManga(ctx, "anilist", "1", "Berserk", "")
	res, err := db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at) VALUES (?, ?, 'reading', ?, ?)`,
		userID, mangaID, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	entryID, _ := res.LastInsertId()

	// 3 caps en 'en' + 1 en 'es'. Solo los 3 'en' cuentan para 'en'-user.
	publishedAt := time.Now().Add(-72 * time.Hour)
	var chFirst int64
	for i, lang := range []string{"en", "en", "en", "es"} {
		r, err := db.Exec(`INSERT INTO chapter (manga_id, source, source_id, number, language, published_at, external_url, created_at)
			VALUES (?, 'mangadex', ?, ?, ?, ?, '', 0)`,
			mangaID, "ch-"+strconv.Itoa(i), strconv.Itoa(i), lang, publishedAt.Add(time.Duration(i)*time.Hour).Unix())
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			chFirst, _ = r.LastInsertId()
		}
	}

	// Sin cursor: Total=3, Unread=3.
	entries, err := h.listEntries(ctx, userID, "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries: %d", len(entries))
	}
	if entries[0].TotalChapters != 3 || entries[0].UnreadCount != 3 {
		t.Errorf("sin cursor: Total=%d Unread=%d (esperado 3/3)", entries[0].TotalChapters, entries[0].UnreadCount)
	}

	// Avanzamos el cursor al primer cap (cap número 1.0): Total=3, Unread=2.
	if _, err := db.Exec(`UPDATE library_entries
		SET last_read_chapter_number = (SELECT CAST(number AS REAL) FROM chapter WHERE id = ?)
		WHERE id = ?`, chFirst, entryID); err != nil {
		t.Fatal(err)
	}
	entries, err = h.listEntries(ctx, userID, "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].TotalChapters != 3 || entries[0].UnreadCount != 2 {
		t.Errorf("con cursor: Total=%d Unread=%d (esperado 3/2)", entries[0].TotalChapters, entries[0].UnreadCount)
	}
}

func TestDedupeCatalogMarcaInLibrary(t *testing.T) {
	results := []mangasource.SearchResult{
		{SourceID: "1", Title: "Berserk"},
		{SourceID: "2", Title: "Vagabond"},
	}
	owned := map[string]bool{"1": true}

	got := dedupeCatalog(results, owned)

	if len(got) != 2 {
		t.Fatalf("esperaba 2 resultados, obtuve %d", len(got))
	}
	if !got[0].InLibrary {
		t.Error("Berserk (source_id 1) debería estar marcado InLibrary")
	}
	if got[1].InLibrary {
		t.Error("Vagabond (source_id 2) NO debería estar marcado InLibrary")
	}
}

func TestOwnedSourceIDs(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	ctx := context.Background()
	userID := insertUser(t, db, "alice@example.com")

	mangaID, err := h.upsertManga(ctx, "anilist", "30002", "Berserk", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.createEntry(ctx, userID, mangaID); err != nil {
		t.Fatal(err)
	}

	owned, err := h.ownedSourceIDs(ctx, userID, "anilist")
	if err != nil {
		t.Fatalf("ownedSourceIDs: %v", err)
	}
	if !owned["30002"] {
		t.Errorf("esperaba que 30002 estuviera en owned, got %v", owned)
	}
	if owned["99999"] {
		t.Error("99999 no debería estar en owned")
	}

	// Aislamiento por source: el mismo manga bajo otra source no aparece.
	ownedOtra, err := h.ownedSourceIDs(ctx, userID, "mangadex")
	if err != nil {
		t.Fatalf("ownedSourceIDs (otra source): %v", err)
	}
	if len(ownedOtra) != 0 {
		t.Errorf("filtro por source roto: esperaba vacío para mangadex, got %v", ownedOtra)
	}

	// Aislamiento por user: otro user sin esa entry no ve el source_id.
	otroUser := insertUser(t, db, "bob@example.com")
	ownedBob, err := h.ownedSourceIDs(ctx, otroUser, "anilist")
	if err != nil {
		t.Fatalf("ownedSourceIDs (otro user): %v", err)
	}
	if len(ownedBob) != 0 {
		t.Errorf("filtro por user roto: esperaba vacío para bob, got %v", ownedBob)
	}
}

func renderList(t *testing.T, h *Handler, userID int64, target string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID}))
	rec := httptest.NewRecorder()
	h.list(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", rec.Code)
	}
	return rec.Body.String()
}

func TestLibraryFiltersLabelled(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	body := renderList(t, h, uid, "/library")

	for _, want := range []string{`for="lib-q"`, `id="lib-q"`, `for="lib-filter"`, `id="lib-filter"`, `<button type="submit">filtrar</button>`} {
		if !strings.Contains(body, want) {
			t.Errorf("filtros de /library sin %q", want)
		}
	}
	for _, bad := range []string{"data-auto-submit", "<noscript>"} {
		if strings.Contains(body, bad) {
			t.Errorf("filtros de /library todavía tienen %q", bad)
		}
	}
}

func TestLayoutSkipLink(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	body := renderList(t, h, uid, "/library")

	i := strings.Index(body, "<body>")
	if i < 0 {
		t.Fatal("sin <body>")
	}
	first := strings.Index(body[i:], "<a ")
	if first < 0 || !strings.HasPrefix(body[i+first:], `<a class="skip-link" href="#contenido">`) {
		t.Error("el primer link del body no es el skip link a #contenido")
	}
	if !strings.Contains(body, `id="contenido"`) {
		t.Error(`falta el destino id="contenido"`)
	}
}

func TestListShowsCursor(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	entryID, _ := insertMangaWithChapters(t, db, uid, "matched")
	insertMangaWithChapters(t, db, uid, "matched")

	if strings.Contains(renderList(t, h, uid, "/library"), "— cap.") {
		t.Error("cursor NULL no debería mostrar — cap.")
	}
	if _, err := db.Exec(`UPDATE library_entries SET last_read_chapter_number = 87 WHERE id = ?`, entryID); err != nil {
		t.Fatal(err)
	}
	if body := renderList(t, h, uid, "/library"); !strings.Contains(body, "— cap. 87<") {
		t.Error("la lista no muestra el cursor — cap. 87")
	}
}

func TestListBadgeVocabulary(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")
	future := time.Now().Add(6 * time.Hour).Unix()

	// 2 nuevos: dos caps, cursor NULL, source polleada.
	_, mNew := insertMangaWithChapters(t, db, uid, "matched")
	insertChapter(t, db, mNew, "n-1", "1", "en", "", time.Now())
	insertChapter(t, db, mNew, "n-2", "2", "en", "", time.Now())
	// al día: un cap, cursor en él.
	eDay, mDay := insertMangaWithChapters(t, db, uid, "matched")
	insertChapter(t, db, mDay, "d-1", "1", "en", "", time.Now())
	// buscando…: matched sin pollear todavía.
	insertMangaWithChapters(t, db, uid, "matched")
	// sin capítulos: no_results.
	insertMangaWithChapters(t, db, uid, "no_results")
	if _, err := db.Exec(`UPDATE library_entries SET last_read_chapter_number = 1 WHERE id = ?`, eDay); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE manga_sources SET next_check_at = ? WHERE manga_id IN (?, ?)`, future, mNew, mDay); err != nil {
		t.Fatal(err)
	}

	body := renderList(t, h, uid, "/library")
	for _, want := range []string{">2 nuevos<", ">al día<", ">buscando…<", ">sin capítulos<"} {
		if !strings.Contains(body, want) {
			t.Errorf("falta el badge %q", want)
		}
	}
	for _, bad := range []string{"pendiente", "matching incierto", "sin source"} {
		if strings.Contains(body, bad) {
			t.Errorf("la lista todavía dice %q", bad)
		}
	}
}

// D4: dentro de un mismo estado, lo que tiene capítulos sin leer va arriba.
func TestListEntriesUnreadFirstWithinStatus(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	uid := insertUser(t, db, "alice@example.com")

	oldID, oldManga := insertMangaWithChapters(t, db, uid, "matched")
	insertChapter(t, db, oldManga, "o-1", "1", "en", "", time.Now())
	newID, _ := insertMangaWithChapters(t, db, uid, "matched")
	if _, err := db.Exec(`UPDATE library_entries SET updated_at = ? WHERE id = ?`, time.Now().Add(-48*time.Hour).Unix(), oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE library_entries SET updated_at = ? WHERE id = ?`, time.Now().Unix(), newID); err != nil {
		t.Fatal(err)
	}

	entries, err := h.listEntries(context.Background(), uid, "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != oldID {
		t.Errorf("la entry con caps sin leer (%d) debería ir primero; orden: %d, %d", oldID, entries[0].ID, entries[1].ID)
	}
}

func TestCatalogResultsShowMetaAndNamedAdd(t *testing.T) {
	db := setupDB(t)
	src := &fakeSource{results: []mangasource.SearchResult{{
		SourceID: "2", Title: "Haikyu!!", AltTitle: "Haikyuu!!",
		Format: "MANGA", Country: "JP", Year: 2012, Chapters: 404, Status: "FINISHED",
	}}}
	h := newFullHandler(t, db, src)
	uid := insertUser(t, db, "alice@example.com")
	body := renderList(t, h, uid, "/library?q=haikyu")

	for _, want := range []string{
		"manga · 2012 · 404 caps · terminado",
		"Haikyuu!!",
		`agregar<span class="visually-hidden"> Haikyu!!</span>`,
		">agregar nuevo<",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("falta %q en los resultados del catálogo", want)
		}
	}
}
