package handler

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/session"
)

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrations.Run(db, os.DirFS("../../../migrations")); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newTestHandler(t *testing.T, db *sql.DB) *Handler {
	t.Helper()
	h, err := New(Config{
		DB:            db,
		HTMLTemplates: os.DirFS("../../../web/templates"),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
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

func insertManga(t *testing.T, db *sql.DB, mangadexID string) int64 {
	t.Helper()
	now := time.Now().UTC().Unix()
	res, err := db.Exec(`
		INSERT INTO manga (source, source_id, title, cover_url, created_at, updated_at)
		VALUES ('anilist', ?, 'Test Manga', '', ?, ?)
	`, mangadexID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO manga_sources (manga_id, source, source_id, status) VALUES (?, 'mangadex', ?, 'matched')
	`, id, mangadexID); err != nil {
		t.Fatal(err)
	}
	return id
}

// insertEntry crea library_entry y opcionalmente setea el cursor de
// lectura. Por compatibilidad con tests existentes, el parámetro sigue
// siendo un puntero a chapter.id — internamente lo resolvemos al number
// del cap para setear last_read_chapter_number (cursor por número desde
// la migration 0008).
func insertEntry(t *testing.T, db *sql.DB, userID, mangaID int64, status string, lastReadChapterID *int64) int64 {
	t.Helper()
	now := time.Now().UTC().Unix()
	res, err := db.Exec(`
		INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, userID, mangaID, status, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if lastReadChapterID != nil {
		if _, err := db.Exec(`UPDATE library_entries
			SET last_read_chapter_number = (SELECT CAST(number AS REAL) FROM chapter WHERE id = ?)
			WHERE id = ?`, *lastReadChapterID, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func insertChapter(t *testing.T, db *sql.DB, mangaID int64, sourceID, number, language string, publishedAt time.Time) int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO chapter (manga_id, source, source_id, number, language, published_at, created_at)
		VALUES (?, 'mangadex', ?, ?, ?, ?, ?)
	`, mangaID, sourceID, number, language, publishedAt.UTC().Unix(), time.Now().UTC().Unix())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestUnreadReleasesReturnsAllWhenNoCursor(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertManga(t, db, "md-1")
	insertEntry(t, db, userID, mangaID, "reading", nil)

	t1 := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 2, 1, 10, 0, 0, 0, time.UTC)
	insertChapter(t, db, mangaID, "ch-1", "1", "en", t1)
	insertChapter(t, db, mangaID, "ch-2", "2", "en", t2)

	releases, err := h.UnreadReleases(context.Background(), userID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 {
		t.Fatalf("esperado 2 caps (sin cursor, todos cuentan); obtenido %d", len(releases))
	}
	// Orden descendente: el más nuevo primero.
	if releases[0].ChapterNumber != "2" {
		t.Errorf("primero esperado cap '2', obtenido %q", releases[0].ChapterNumber)
	}
	if releases[1].ChapterNumber != "1" {
		t.Errorf("segundo esperado cap '1', obtenido %q", releases[1].ChapterNumber)
	}
}

func TestUnreadReleasesRespectsCursor(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertManga(t, db, "md-1")

	t1 := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 2, 1, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC)
	ch1 := insertChapter(t, db, mangaID, "ch-1", "1", "en", t1)
	insertChapter(t, db, mangaID, "ch-2", "2", "en", t2)
	insertChapter(t, db, mangaID, "ch-3", "3", "en", t3)

	// Cursor en cap 1: solo 2 y 3 deberían aparecer.
	insertEntry(t, db, userID, mangaID, "reading", &ch1)

	releases, err := h.UnreadReleases(context.Background(), userID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 {
		t.Fatalf("esperado 2 caps post-cursor, obtenido %d", len(releases))
	}
	nums := []string{releases[0].ChapterNumber, releases[1].ChapterNumber}
	if nums[0] != "3" || nums[1] != "2" {
		t.Errorf("orden esperado [3, 2]; obtenido %v", nums)
	}
}

func TestUnreadReleasesSkipsNonReadingStatus(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertManga(t, db, "md-1")
	insertEntry(t, db, userID, mangaID, "planned", nil)

	insertChapter(t, db, mangaID, "ch-1", "1", "en", time.Now())

	releases, err := h.UnreadReleases(context.Background(), userID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 0 {
		t.Errorf("manga con status=planned no debería aparecer; obtenido %d caps", len(releases))
	}
}

func TestUnreadReleasesFiltersByPreferredLanguage(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	// User prefiere español.
	if _, err := db.Exec(`UPDATE users SET preferred_language = 'es' WHERE id = ?`, userID); err != nil {
		t.Fatal(err)
	}
	mangaID := insertManga(t, db, "md-1")
	insertEntry(t, db, userID, mangaID, "reading", nil)

	now := time.Now()
	insertChapter(t, db, mangaID, "ch-en", "1", "en", now)
	insertChapter(t, db, mangaID, "ch-es", "1", "es", now)
	insertChapter(t, db, mangaID, "ch-pt", "1", "pt-br", now)

	releases, err := h.UnreadReleases(context.Background(), userID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 {
		t.Fatalf("esperado 1 cap (solo español); obtenido %d", len(releases))
	}
	if releases[0].Language != "es" {
		t.Errorf("language esperado 'es', obtenido %q", releases[0].Language)
	}
}

func TestUnreadReleasesUsesExternalURLWhenPresent(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertManga(t, db, "md-1")
	insertEntry(t, db, userID, mangaID, "reading", nil)

	now := time.Now().UTC().Unix()
	if _, err := db.Exec(`
		INSERT INTO chapter (manga_id, source, source_id, number, language, published_at, external_url, created_at)
		VALUES (?, 'mangadex', 'ch-ext', '1', 'en', ?, 'https://mangaplus.shueisha.co.jp/x', ?)
	`, mangaID, time.Now().Unix(), now); err != nil {
		t.Fatal(err)
	}

	releases, err := h.UnreadReleases(context.Background(), userID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 {
		t.Fatalf("releases: %d", len(releases))
	}
	if releases[0].ReaderURL != "https://mangaplus.shueisha.co.jp/x" {
		t.Errorf("ReaderURL debería usar externalUrl; obtenido %q", releases[0].ReaderURL)
	}
}

func TestUnreadReleasesBuildsMangadexURLByDefault(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertManga(t, db, "md-1")
	insertEntry(t, db, userID, mangaID, "reading", nil)
	insertChapter(t, db, mangaID, "ch-uuid-123", "1", "en", time.Now())

	releases, err := h.UnreadReleases(context.Background(), userID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if releases[0].ReaderURL != "https://mangadex.org/chapter/ch-uuid-123" {
		t.Errorf("ReaderURL: %q", releases[0].ReaderURL)
	}
}

func TestListRedirectsAnonymous(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)

	req := httptest.NewRequest(http.MethodGet, "/releases", nil)
	rec := httptest.NewRecorder()
	h.list(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: esperado 303, obtenido %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/auth" {
		t.Errorf("location: %q", loc)
	}
}

func TestListRendersWithSession(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertManga(t, db, "md-1")
	// Updatear título del manga para verificar que aparece renderizado.
	if _, err := db.Exec(`UPDATE manga SET title = 'Chainsaw Man' WHERE id = ?`, mangaID); err != nil {
		t.Fatal(err)
	}
	insertEntry(t, db, userID, mangaID, "reading", nil)
	insertChapter(t, db, mangaID, "ch-1", "97", "en", time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodGet, "/releases", nil)
	ctx := session.NewContext(req.Context(), &session.Session{UserID: userID})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.list(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Chainsaw Man") {
		t.Error("body no contiene el título del manga")
	}
	if !strings.Contains(body, "97") {
		t.Error("body no contiene el número de capítulo")
	}
	if !strings.Contains(body, "https://mangadex.org/chapter/ch-1") {
		t.Error("body no contiene el link al reader")
	}
}

func TestListShowsEmptyMessageWhenNoReleases(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")

	req := httptest.NewRequest(http.MethodGet, "/releases", nil)
	req = req.WithContext(session.NewContext(req.Context(), &session.Session{UserID: userID}))
	rec := httptest.NewRecorder()
	h.list(rec, req)

	if !strings.Contains(rec.Body.String(), "al día") {
		t.Errorf("esperado mensaje de empty state; body=%s", rec.Body.String())
	}
}

// --- markRead ---

func markReadRequest(userID, chapterID int64) *http.Request {
	form := url.Values{}
	form.Set("chapter_id", strconv.FormatInt(chapterID, 10))
	req := httptest.NewRequest(http.MethodPost, "/releases/mark-read", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(session.NewContext(req.Context(), &session.Session{UserID: userID}))
	return req
}

func cursorOfEntry(t *testing.T, db *sql.DB, entryID int64) sql.NullFloat64 {
	t.Helper()
	var v sql.NullFloat64
	if err := db.QueryRow(`SELECT last_read_chapter_number FROM library_entries WHERE id = ?`, entryID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMarkReadAdvancesCursor(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertManga(t, db, "md-1")
	entryID := insertEntry(t, db, userID, mangaID, "reading", nil)
	chID := insertChapter(t, db, mangaID, "ch-1", "1", "en", time.Now())

	rec := httptest.NewRecorder()
	h.markRead(rec, markReadRequest(userID, chID))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: esperado 303, obtenido %d (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/releases" {
		t.Errorf("location: %q", loc)
	}
	// Cursor por número: el cap insertado tiene number="1" → cursor=1.0.
	got := cursorOfEntry(t, db, entryID)
	if !got.Valid || got.Float64 != 1.0 {
		t.Errorf("cursor: esperado 1.0 (number del cap %d), obtenido %+v", chID, got)
	}
}

func TestMarkReadRejectsChapterFromUnownedManga(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	// Alice tiene manga A.
	mangaA := insertManga(t, db, "md-A")
	insertEntry(t, db, userID, mangaA, "reading", nil)
	// Bob tiene manga B con un cap; Alice intenta marcarlo.
	bobID := insertUser(t, db, "bob@example.com")
	mangaB := insertManga(t, db, "md-B")
	insertEntry(t, db, bobID, mangaB, "reading", nil)
	chB := insertChapter(t, db, mangaB, "ch-B-1", "1", "en", time.Now())

	rec := httptest.NewRecorder()
	h.markRead(rec, markReadRequest(userID, chB))

	if rec.Code != http.StatusNotFound {
		t.Errorf("esperado 404 al marcar cap de manga ajeno, obtenido %d", rec.Code)
	}
}

func TestMarkReadRejectsNonExistentChapter(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertManga(t, db, "md-1")
	insertEntry(t, db, userID, mangaID, "reading", nil)

	rec := httptest.NewRecorder()
	h.markRead(rec, markReadRequest(userID, 99999))

	if rec.Code != http.StatusNotFound {
		t.Errorf("esperado 404 con chapter_id inexistente, obtenido %d", rec.Code)
	}
}

func TestMarkReadRejectsAnonymous(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)

	form := url.Values{}
	form.Set("chapter_id", "1")
	req := httptest.NewRequest(http.MethodPost, "/releases/mark-read", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.markRead(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/auth" {
		t.Errorf("anónimo: esperado 303 -> /auth; obtenido %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestMarkReadRejectsInvalidChapterID(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")

	form := url.Values{}
	form.Set("chapter_id", "abc")
	req := httptest.NewRequest(http.MethodPost, "/releases/mark-read", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(session.NewContext(req.Context(), &session.Session{UserID: userID}))
	rec := httptest.NewRecorder()
	h.markRead(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("esperado 400 con chapter_id no numérico, obtenido %d", rec.Code)
	}
}

// TestMarkReadHidesChapterFromSubsequentList verifica el integration completo:
// después de marcar el cap, ya no aparece en /releases para ese user.
func TestMarkReadHidesChapterFromSubsequentList(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertManga(t, db, "md-1")
	insertEntry(t, db, userID, mangaID, "reading", nil)
	ch1 := insertChapter(t, db, mangaID, "ch-1", "1", "en", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	insertChapter(t, db, mangaID, "ch-2", "2", "en", time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC))

	// Antes: ambos caps aparecen.
	releases, err := h.UnreadReleases(context.Background(), userID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 {
		t.Fatalf("antes de marcar: esperado 2 caps, obtenido %d", len(releases))
	}

	// Marcar cap 1 como leído.
	rec := httptest.NewRecorder()
	h.markRead(rec, markReadRequest(userID, ch1))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("markRead status: %d", rec.Code)
	}

	// Después: solo cap 2 debería aparecer.
	releases, err = h.UnreadReleases(context.Background(), userID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 {
		t.Fatalf("después de marcar: esperado 1 cap, obtenido %d", len(releases))
	}
	if releases[0].ChapterNumber != "2" {
		t.Errorf("cap restante esperado '2', obtenido %q", releases[0].ChapterNumber)
	}
}
