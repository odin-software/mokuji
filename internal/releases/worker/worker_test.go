package worker

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/mangadex"
	"git.odin.do/kengru/mokuji/internal/migrations"
)

// fakeFetcher simula al cliente MangaDex con respuestas programables por
// mangadexID y captura los args de las llamadas para verificación.
type fakeFetcher struct {
	feed   map[string][]mangadex.Chapter
	err    error
	calls  []fetchCall
	errFor map[string]error // override de error por mangadexID
}

type fetchCall struct {
	MangaID  string
	Language string
	Since    time.Time
}

func (f *fakeFetcher) FetchFeed(_ context.Context, id, lang string, since time.Time) ([]mangadex.Chapter, error) {
	f.calls = append(f.calls, fetchCall{id, lang, since})
	if e, ok := f.errFor[id]; ok {
		return nil, e
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.feed[id], nil
}

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

func insertUser(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO users (email, created_at) VALUES (?, ?)`, email, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// insertMatchedManga crea un manga con mangadex_status='matched' y devuelve su id.
// nextCheckAt: 0 = NULL (nunca chequeado); negativo = vencido hace N segundos;
// positivo = vence en el futuro.
func insertMatchedManga(t *testing.T, db *sql.DB, mangadexID string, nextCheckOffsetSec int64) int64 {
	t.Helper()
	now := time.Now().UTC().Unix()
	var nextCheck any
	if nextCheckOffsetSec != 0 {
		nextCheck = now + nextCheckOffsetSec
	}
	res, err := db.Exec(`
		INSERT INTO manga (source, source_id, title, cover_url, created_at, updated_at,
		                   mangadex_id, mangadex_status, mangadex_next_check_at)
		VALUES ('anilist', ?, 'Test', '', ?, ?, ?, 'matched', ?)
	`, mangadexID, now, now, mangadexID, nextCheck)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func addReadingEntry(t *testing.T, db *sql.DB, userID, mangaID int64) {
	t.Helper()
	now := time.Now().UTC().Unix()
	_, err := db.Exec(`
		INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at)
		VALUES (?, ?, 'reading', ?, ?)
	`, userID, mangaID, now, now)
	if err != nil {
		t.Fatal(err)
	}
}

func newTestWorker(t *testing.T, db *sql.DB, client FeedFetcher) *Worker {
	t.Helper()
	w, err := New(Config{
		DB:     db,
		Client: client,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func countChapters(t *testing.T, db *sql.DB, mangaID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chapter WHERE manga_id = ?`, mangaID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func nextCheckAt(t *testing.T, db *sql.DB, mangaID int64) (sql.NullInt64, sql.NullInt64) {
	t.Helper()
	var nextCheck, checkedAt sql.NullInt64
	if err := db.QueryRow(`SELECT mangadex_next_check_at, mangadex_checked_at FROM manga WHERE id = ?`, mangaID).Scan(&nextCheck, &checkedAt); err != nil {
		t.Fatal(err)
	}
	return nextCheck, checkedAt
}

// --- Tests ---

func TestRunOnceInsertsChaptersAndSchedulesNext(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", -3600) // venció hace una hora
	addReadingEntry(t, db, userID, mangaID)

	publishedAt := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{feed: map[string][]mangadex.Chapter{
		"md-1": {
			{ID: "ch-1", Number: "1", Title: "First", Language: "en", PublishedAt: publishedAt},
			{ID: "ch-2", Number: "2", Title: "Second", Language: "en", PublishedAt: publishedAt.Add(time.Hour)},
		},
	}}
	w := newTestWorker(t, db, fetcher)

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := countChapters(t, db, mangaID); got != 2 {
		t.Errorf("chapters insertados: esperado 2, obtenido %d", got)
	}

	nextCheck, checkedAt := nextCheckAt(t, db, mangaID)
	if !nextCheck.Valid {
		t.Fatal("next_check_at debería estar poblado")
	}
	if d := nextCheck.Int64 - time.Now().UTC().Unix(); d < 5*3600 || d > 7*3600 {
		t.Errorf("next_check_at fuera del rango esperado (~6h): %d seg", d)
	}
	if !checkedAt.Valid {
		t.Error("checked_at debería estar poblado")
	}

	if len(fetcher.calls) != 1 {
		t.Fatalf("calls a FetchFeed: esperado 1, obtenido %d", len(fetcher.calls))
	}
	if !fetcher.calls[0].Since.IsZero() {
		t.Errorf("primer poll debería tener since=zero, obtenido %v", fetcher.calls[0].Since)
	}
}

func TestRunOnceSkipsMangasWithoutReadingEntries(t *testing.T) {
	db := setupDB(t)
	mangaID := insertMatchedManga(t, db, "md-1", -3600)
	// NO addReadingEntry — nadie tiene este manga en reading.

	fetcher := &fakeFetcher{feed: map[string][]mangadex.Chapter{
		"md-1": {{ID: "ch-1", PublishedAt: time.Now()}},
	}}
	w := newTestWorker(t, db, fetcher)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := countChapters(t, db, mangaID); got != 0 {
		t.Errorf("no debería pollar manga sin reading entries; chapters=%d", got)
	}
	if len(fetcher.calls) != 0 {
		t.Errorf("no debería llamar al fetcher; calls=%v", fetcher.calls)
	}
}

func TestRunOnceSkipsNonDueMangas(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", 3600) // vence en una hora (futuro)
	addReadingEntry(t, db, userID, mangaID)

	fetcher := &fakeFetcher{feed: map[string][]mangadex.Chapter{
		"md-1": {{ID: "ch-1", PublishedAt: time.Now()}},
	}}
	w := newTestWorker(t, db, fetcher)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fetcher.calls) != 0 {
		t.Errorf("no debería pollar manga no vencido; calls=%v", fetcher.calls)
	}
}

func TestRunOnceSkipsNonMatchedMangas(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	// Manga con mangadex_status='needs_review' (no es matched).
	now := time.Now().UTC().Unix()
	res, err := db.Exec(`
		INSERT INTO manga (source, source_id, title, cover_url, created_at, updated_at, mangadex_status)
		VALUES ('anilist', 'x', 't', '', ?, ?, 'needs_review')
	`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	mangaID, _ := res.LastInsertId()
	addReadingEntry(t, db, userID, mangaID)

	fetcher := &fakeFetcher{}
	w := newTestWorker(t, db, fetcher)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fetcher.calls) != 0 {
		t.Errorf("no debería pollar manga sin status=matched; calls=%v", fetcher.calls)
	}
}

func TestRunOnceDeduplicatesChaptersOnSecondTick(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", -3600)
	addReadingEntry(t, db, userID, mangaID)

	publishedAt := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{feed: map[string][]mangadex.Chapter{
		"md-1": {
			{ID: "ch-1", Number: "1", Language: "en", PublishedAt: publishedAt},
		},
	}}
	w := newTestWorker(t, db, fetcher)

	// Primer tick: inserta 1.
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Forzar que vuelva a estar vencido para el segundo tick.
	_, _ = db.Exec(`UPDATE manga SET mangadex_next_check_at = ? WHERE id = ?`, time.Now().Unix()-100, mangaID)

	// Segundo tick: el cliente devuelve el mismo cap; ON CONFLICT DO NOTHING.
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countChapters(t, db, mangaID); got != 1 {
		t.Errorf("dedupe falló: esperado 1, obtenido %d", got)
	}
}

func TestRunOncePassesSinceFromLatestPublishedAt(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", -3600)
	addReadingEntry(t, db, userID, mangaID)

	// Pre-insertar un cap antiguo para que el siguiente poll use ese published_at + 1s como since.
	publishedAt := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	now := time.Now().UTC().Unix()
	_, err := db.Exec(`
		INSERT INTO chapter (manga_id, source, source_id, number, language, published_at, created_at)
		VALUES (?, 'mangadex', 'old-ch', '1', 'en', ?, ?)
	`, mangaID, publishedAt.Unix(), now)
	if err != nil {
		t.Fatal(err)
	}

	fetcher := &fakeFetcher{feed: map[string][]mangadex.Chapter{"md-1": nil}}
	w := newTestWorker(t, db, fetcher)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(fetcher.calls) != 1 {
		t.Fatalf("calls: esperado 1, obtenido %d", len(fetcher.calls))
	}
	wantSince := publishedAt.Add(time.Second).UTC()
	if !fetcher.calls[0].Since.Equal(wantSince) {
		t.Errorf("since: esperado %v, obtenido %v", wantSince, fetcher.calls[0].Since)
	}
}

func TestRunOnceRateLimitedReschedulesShorter(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", -3600)
	addReadingEntry(t, db, userID, mangaID)

	fetcher := &fakeFetcher{errFor: map[string]error{"md-1": mangadex.ErrRateLimited}}
	w := newTestWorker(t, db, fetcher)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	nextCheck, _ := nextCheckAt(t, db, mangaID)
	if !nextCheck.Valid {
		t.Fatal("next_check_at debería estar poblado tras rate limit")
	}
	d := nextCheck.Int64 - time.Now().UTC().Unix()
	// Esperamos ~RateLimitedDelay (15min), no el RecheckInterval normal (6h).
	if d > int64(20*time.Minute/time.Second) || d < int64(10*time.Minute/time.Second) {
		t.Errorf("next_check_at tras rate limit fuera del rango ~15min: %d seg", d)
	}
}

func TestRunOnceContinuesAfterOneMangaFails(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	failID := insertMatchedManga(t, db, "md-fail", -3600)
	okID := insertMatchedManga(t, db, "md-ok", -3600)
	addReadingEntry(t, db, userID, failID)
	addReadingEntry(t, db, userID, okID)

	fetcher := &fakeFetcher{
		errFor: map[string]error{"md-fail": errors.New("network down")},
		feed: map[string][]mangadex.Chapter{
			"md-ok": {{ID: "ch-ok", PublishedAt: time.Now()}},
		},
	}
	w := newTestWorker(t, db, fetcher)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countChapters(t, db, okID); got != 1 {
		t.Errorf("segundo manga debería procesarse aunque el primero falle; chapters=%d", got)
	}
	// El que falló no debería tener caps ni next_check_at actualizado.
	if got := countChapters(t, db, failID); got != 0 {
		t.Errorf("manga fallido no debería tener caps; got %d", got)
	}
}

func TestRunOnceBatchLimit(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")

	// Crear BatchSize + 5 mangas vencidos y con reading entry.
	total := BatchSize + 5
	for i := range total {
		mangaID := insertMatchedManga(t, db, mdID(i), -3600)
		addReadingEntry(t, db, userID, mangaID)
	}

	fetcher := &fakeFetcher{feed: map[string][]mangadex.Chapter{}}
	w := newTestWorker(t, db, fetcher)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fetcher.calls) != BatchSize {
		t.Errorf("primer tick debería procesar exactamente BatchSize=%d, obtuvo %d", BatchSize, len(fetcher.calls))
	}
}

func mdID(i int) string {
	const hex = "0123456789abcdef"
	b := []byte("00000000-0000-0000-0000-000000000000")
	for j := len(b) - 1; j >= 0 && i > 0; j-- {
		if b[j] == '-' {
			continue
		}
		b[j] = hex[i%16]
		i /= 16
	}
	return string(b)
}

func TestNewValidatesDependencies(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Error("DB requerido debería fallar")
	}
	_, err = New(Config{DB: &sql.DB{}})
	if err == nil {
		t.Error("Client requerido debería fallar")
	}
}
