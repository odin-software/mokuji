package worker

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/releasesource"
)

// fakeClient implementa releasesource.Client con respuestas programables y
// captura los args de cada llamada para verificación.
type fakeClient struct {
	name    string
	feed    map[string][]releasesource.Chapter
	err     error
	errFor  map[string]error
	calls   []fetchCall
	queries []string
	results []releasesource.MangaResult
}

type fetchCall struct {
	SourceID string
	Language string
	Since    time.Time
}

func (f *fakeClient) Name() string {
	if f.name == "" {
		return "mangadex"
	}
	return f.name
}

func (f *fakeClient) Search(_ context.Context, q string) ([]releasesource.MangaResult, error) {
	f.queries = append(f.queries, q)
	return f.results, f.err
}

func (f *fakeClient) FetchChapters(_ context.Context, id, lang string, since time.Time) ([]releasesource.Chapter, error) {
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

// insertMatchedManga crea un manga + fila manga_sources con
// source='mangadex' y status='matched'. nextCheckAt: 0 = NULL (nunca
// chequeado); negativo = vencido hace N segundos; positivo = vence en el
// futuro.
func insertMatchedManga(t *testing.T, db *sql.DB, sourceID string, nextCheckOffsetSec int64) int64 {
	t.Helper()
	now := time.Now().UTC().Unix()
	res, err := db.Exec(`
		INSERT INTO manga (source, source_id, title, cover_url, created_at, updated_at)
		VALUES ('anilist', ?, 'Test', '', ?, ?)
	`, sourceID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	mangaID, _ := res.LastInsertId()

	var nextCheck any
	if nextCheckOffsetSec != 0 {
		nextCheck = now + nextCheckOffsetSec
	}
	_, err = db.Exec(`
		INSERT INTO manga_sources (manga_id, source, source_id, status, next_check_at)
		VALUES (?, 'mangadex', ?, 'matched', ?)
	`, mangaID, sourceID, nextCheck)
	if err != nil {
		t.Fatal(err)
	}
	return mangaID
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

func newTestWorker(t *testing.T, db *sql.DB, client *fakeClient) *Worker {
	t.Helper()
	w, err := New(Config{
		DB:      db,
		Clients: map[string]releasesource.Client{client.Name(): client},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
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
	if err := db.QueryRow(`
		SELECT next_check_at, checked_at FROM manga_sources WHERE manga_id = ? AND source = 'mangadex'
	`, mangaID).Scan(&nextCheck, &checkedAt); err != nil {
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
	client := &fakeClient{feed: map[string][]releasesource.Chapter{
		"md-1": {
			{ID: "ch-1", Number: "1", Title: "First", Language: "en", PublishedAt: publishedAt},
			{ID: "ch-2", Number: "2", Title: "Second", Language: "en", PublishedAt: publishedAt.Add(time.Hour)},
		},
	}}
	w := newTestWorker(t, db, client)

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

	if len(client.calls) != 1 {
		t.Fatalf("calls a FetchChapters: esperado 1, obtenido %d", len(client.calls))
	}
	if !client.calls[0].Since.IsZero() {
		t.Errorf("primer poll debería tener since=zero, obtenido %v", client.calls[0].Since)
	}
}

func TestRunOnceSkipsMangasWithoutReadingEntries(t *testing.T) {
	db := setupDB(t)
	mangaID := insertMatchedManga(t, db, "md-1", -3600)
	// NO addReadingEntry — nadie tiene este manga en reading.

	client := &fakeClient{feed: map[string][]releasesource.Chapter{
		"md-1": {{ID: "ch-1", PublishedAt: time.Now()}},
	}}
	w := newTestWorker(t, db, client)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := countChapters(t, db, mangaID); got != 0 {
		t.Errorf("no debería pollar manga sin reading entries; chapters=%d", got)
	}
	if len(client.calls) != 0 {
		t.Errorf("no debería llamar al fetcher; calls=%v", client.calls)
	}
}

func TestRunOnceSkipsNonDueMangas(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", 3600) // vence en una hora (futuro)
	addReadingEntry(t, db, userID, mangaID)

	client := &fakeClient{feed: map[string][]releasesource.Chapter{
		"md-1": {{ID: "ch-1", PublishedAt: time.Now()}},
	}}
	w := newTestWorker(t, db, client)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 0 {
		t.Errorf("no debería pollar manga no vencido; calls=%v", client.calls)
	}
}

func TestRunOnceSkipsNonMatchedSources(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	// Manga con source en needs_review (no es matched).
	now := time.Now().UTC().Unix()
	res, err := db.Exec(`
		INSERT INTO manga (source, source_id, title, cover_url, created_at, updated_at)
		VALUES ('anilist', 'x', 't', '', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	mangaID, _ := res.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO manga_sources (manga_id, source, source_id, status)
		VALUES (?, 'mangadex', '', 'needs_review')
	`, mangaID); err != nil {
		t.Fatal(err)
	}
	addReadingEntry(t, db, userID, mangaID)

	client := &fakeClient{}
	w := newTestWorker(t, db, client)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 0 {
		t.Errorf("no debería pollar source sin status=matched; calls=%v", client.calls)
	}
}

func TestRunOnceDeduplicatesChaptersOnSecondTick(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", -3600)
	addReadingEntry(t, db, userID, mangaID)

	publishedAt := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	client := &fakeClient{feed: map[string][]releasesource.Chapter{
		"md-1": {
			{ID: "ch-1", Number: "1", Language: "en", PublishedAt: publishedAt},
		},
	}}
	w := newTestWorker(t, db, client)

	// Primer tick: inserta 1.
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Forzar que vuelva a estar vencido para el segundo tick.
	_, _ = db.Exec(`
		UPDATE manga_sources SET next_check_at = ? WHERE manga_id = ? AND source = 'mangadex'
	`, time.Now().Unix()-100, mangaID)

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

	client := &fakeClient{feed: map[string][]releasesource.Chapter{"md-1": nil}}
	w := newTestWorker(t, db, client)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(client.calls) != 1 {
		t.Fatalf("calls: esperado 1, obtenido %d", len(client.calls))
	}
	wantSince := publishedAt.Add(time.Second).UTC()
	if !client.calls[0].Since.Equal(wantSince) {
		t.Errorf("since: esperado %v, obtenido %v", wantSince, client.calls[0].Since)
	}
}

func TestRunOnceRateLimitedReschedulesShorter(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", -3600)
	addReadingEntry(t, db, userID, mangaID)

	client := &fakeClient{errFor: map[string]error{"md-1": releasesource.ErrRateLimited}}
	w := newTestWorker(t, db, client)
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

	client := &fakeClient{
		errFor: map[string]error{"md-fail": errors.New("network down")},
		feed: map[string][]releasesource.Chapter{
			"md-ok": {{ID: "ch-ok", PublishedAt: time.Now()}},
		},
	}
	w := newTestWorker(t, db, client)
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

	client := &fakeClient{feed: map[string][]releasesource.Chapter{}}
	w := newTestWorker(t, db, client)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != BatchSize {
		t.Errorf("primer tick debería procesar exactamente BatchSize=%d, obtuvo %d", BatchSize, len(client.calls))
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
		t.Error("Clients requerido debería fallar")
	}
}

func TestPollOneBypassesDueQuery(t *testing.T) {
	// PollOne (público, usado por library.add para fetch sincrónico tras
	// matchear) debe operar incluso si next_check_at está en el futuro y
	// sin verificar el filtro EXISTS reading entries — la idea es que el
	// caller ya validó que tiene sentido pollear.
	db := setupDB(t)
	mangaID := insertMatchedManga(t, db, "md-poll", 7200) // vence en 2h (no due)
	// Nota: NO agregamos library_entry; PollOne debe correr igual.

	publishedAt := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	client := &fakeClient{feed: map[string][]releasesource.Chapter{
		"md-poll": {
			{ID: "ch-1", Number: "1", Language: "en", PublishedAt: publishedAt},
		},
	}}
	w := newTestWorker(t, db, client)

	if err := w.PollOne(context.Background(), mangaID, "mangadex", "md-poll"); err != nil {
		t.Fatalf("PollOne: %v", err)
	}
	if got := countChapters(t, db, mangaID); got != 1 {
		t.Errorf("chapters insertados: esperado 1, obtenido %d", got)
	}
}

func TestPollOnePropagatesRateLimitedError(t *testing.T) {
	db := setupDB(t)
	mangaID := insertMatchedManga(t, db, "md-rl", 0)
	client := &fakeClient{err: releasesource.ErrRateLimited}
	w := newTestWorker(t, db, client)

	err := w.PollOne(context.Background(), mangaID, "mangadex", "md-rl")
	// pollSource maneja ErrRateLimited reagendando, no propagando — verificar
	// que next_check_at quedó en el futuro cercano (RateLimitedDelay).
	if err != nil {
		t.Errorf("PollOne debe manejar ErrRateLimited internamente: %v", err)
	}
	nextCheck, _ := nextCheckAt(t, db, mangaID)
	if !nextCheck.Valid {
		t.Fatal("next_check_at no fue seteado tras rate limit")
	}
	delta := nextCheck.Int64 - time.Now().UTC().Unix()
	if delta < 60 || delta > 30*60 {
		t.Errorf("next_check_at tras rate limit fuera de rango (~15min): %d seg", delta)
	}
}

func TestPollOneErrsOnUnknownSource(t *testing.T) {
	db := setupDB(t)
	mangaID := insertMatchedManga(t, db, "md-x", 0)
	client := &fakeClient{}
	w := newTestWorker(t, db, client)

	err := w.PollOne(context.Background(), mangaID, "asura", "x")
	if err == nil {
		t.Fatal("source no cableada debería devolver error")
	}
}

// --- SourceHealth tracking ---

func TestHealthInitializedForAllConfiguredSources(t *testing.T) {
	// Al construir, el worker debe inicializar entradas zero por cada
	// source — para que /admin/sources-health muestre las configuradas
	// desde el día 0, incluso si nunca se llamaron.
	db := setupDB(t)
	w, err := New(Config{
		DB: db,
		Clients: map[string]releasesource.Client{
			"mangadex": &fakeClient{name: "mangadex"},
			"asura":    &fakeClient{name: "asura"},
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := w.Health()
	if len(h) != 2 {
		t.Fatalf("health: esperado 2 entradas, obtenido %d", len(h))
	}
	for _, src := range []string{"mangadex", "asura"} {
		entry, ok := h[src]
		if !ok {
			t.Errorf("%q ausente del health snapshot", src)
			continue
		}
		if !entry.LastSuccessAt.IsZero() || !entry.LastErrorAt.IsZero() {
			t.Errorf("%q debería tener zero al arranque, got %+v", src, entry)
		}
	}
}

func TestHealthRecordsSuccessAndResetsFailures(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", -3600)
	addReadingEntry(t, db, userID, mangaID)

	client := &fakeClient{feed: map[string][]releasesource.Chapter{
		"md-1": {{ID: "ch-1", Number: "1", Language: "en", PublishedAt: time.Now()}},
	}}
	w := newTestWorker(t, db, client)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	h := w.Health()["mangadex"]
	if h.LastSuccessAt.IsZero() {
		t.Error("LastSuccessAt no fue seteado tras éxito")
	}
	if h.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures: %d, want 0", h.ConsecutiveFailures)
	}
	if h.LastError != "" {
		t.Errorf("LastError debería estar vacío tras éxito, got %q", h.LastError)
	}
}

func TestHealthRecordsErrorAndIncrementsFailures(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", -3600)
	addReadingEntry(t, db, userID, mangaID)

	client := &fakeClient{err: errors.New("network down")}
	w := newTestWorker(t, db, client)

	// Dos ticks consecutivos con error → ConsecutiveFailures debe ser 2.
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Hay que vencer la fila a mano: desde el backoff persistente, un fallo
	// la reagenda hacia el futuro en vez de dejarla permanentemente vencida.
	// Esto simula el siguiente tick una vez cumplido el backoff.
	if _, err := db.Exec(
		`UPDATE manga_sources SET next_check_at = ? WHERE manga_id = ?`,
		time.Now().UTC().Unix()-1, mangaID,
	); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	h := w.Health()["mangadex"]
	if h.LastErrorAt.IsZero() {
		t.Error("LastErrorAt no fue seteado")
	}
	if h.ConsecutiveFailures < 2 {
		t.Errorf("ConsecutiveFailures: %d, want >=2", h.ConsecutiveFailures)
	}
	if h.LastError == "" || !strings.Contains(h.LastError, "network down") {
		t.Errorf("LastError debería contener el mensaje, got %q", h.LastError)
	}
}

func TestHealthDoesNotCountRateLimitAsFailure(t *testing.T) {
	// Rate limit es backpressure, no degradación de la source — no
	// debería incrementar ConsecutiveFailures ni setear LastError.
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", -3600)
	addReadingEntry(t, db, userID, mangaID)

	client := &fakeClient{errFor: map[string]error{"md-1": releasesource.ErrRateLimited}}
	w := newTestWorker(t, db, client)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	h := w.Health()["mangadex"]
	if h.ConsecutiveFailures != 0 {
		t.Errorf("rate limit NO debería incrementar ConsecutiveFailures, got %d", h.ConsecutiveFailures)
	}
	if h.LastError != "" {
		t.Errorf("rate limit NO debería poblar LastError, got %q", h.LastError)
	}
}

// --- Backoff persistente por fila (migración 0014) ---

func sourceFailureState(t *testing.T, db *sql.DB, mangaID int64) (int, sql.NullString) {
	t.Helper()
	var n int
	var lastErr sql.NullString
	if err := db.QueryRow(`
		SELECT consecutive_failures, last_error FROM manga_sources
		WHERE manga_id = ? AND source = 'mangadex'
	`, mangaID).Scan(&n, &lastErr); err != nil {
		t.Fatal(err)
	}
	return n, lastErr
}

// TestFalloReagendaLaFila es el test central del bug: antes, un fallo que no
// fuera rate limit volvía sin tocar next_check_at. Para una fila nueva eso
// significaba dejarla en NULL, y como la cola ordena NULLs primero, quedaba
// al frente de todos los batches para siempre.
func TestFalloReagendaLaFila(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "a@example.com")
	mangaID := insertMatchedManga(t, db, "md-roto", 0) // next_check_at NULL
	addReadingEntry(t, db, userID, mangaID)

	client := &fakeClient{errFor: map[string]error{"md-roto": errors.New("boom")}}
	w := newTestWorker(t, db, client)

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	nextCheck, _ := nextCheckAt(t, db, mangaID)
	if !nextCheck.Valid {
		t.Fatal("next_check_at quedó NULL tras el fallo; la fila vuelve a la cabeza de cada batch")
	}
	if nextCheck.Int64 <= time.Now().UTC().Unix() {
		t.Errorf("next_check_at quedó en el pasado (%d); la fila sigue vencida", nextCheck.Int64)
	}

	fails, lastErr := sourceFailureState(t, db, mangaID)
	if fails != 1 {
		t.Errorf("esperaba consecutive_failures=1, got %d", fails)
	}
	if !lastErr.Valid || !strings.Contains(lastErr.String, "boom") {
		t.Errorf("last_error no guardó el motivo: %v", lastErr)
	}
}

// TestFilaRotaNoMonopolizaElBatch es la regresión de la hambruna: tras una
// pasada, ninguna fila debe seguir vencida — ni la que falló.
func TestFilaRotaNoMonopolizaElBatch(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "a@example.com")
	rota := insertMatchedManga(t, db, "md-roto", 0)
	sana := insertMatchedManga(t, db, "md-sana", 0)
	addReadingEntry(t, db, userID, rota)
	addReadingEntry(t, db, userID, sana)

	client := &fakeClient{errFor: map[string]error{"md-roto": errors.New("404 upstream")}}
	w := newTestWorker(t, db, client)

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	pendientes, err := w.dueSources(context.Background(), 10)
	if err != nil {
		t.Fatalf("dueSources: %v", err)
	}
	if len(pendientes) != 0 {
		t.Errorf("quedaron %d filas vencidas tras la pasada; la rota reaparece en el próximo batch", len(pendientes))
	}
}

func TestBackoffCreceYSeTopa(t *testing.T) {
	db := setupDB(t)
	w := newTestWorker(t, db, &fakeClient{})

	casos := []struct {
		fallos int
		quiero time.Duration
	}{
		{1, DefaultRecheckInterval},
		{2, 2 * DefaultRecheckInterval},
		{3, 4 * DefaultRecheckInterval},
		{4, MaxBackoff},
		{50, MaxBackoff},
	}
	for _, c := range casos {
		if got := w.backoffFor(c.fallos); got != c.quiero {
			t.Errorf("backoffFor(%d) = %v, quiero %v", c.fallos, got, c.quiero)
		}
	}
	if 4*DefaultRecheckInterval > MaxBackoff {
		t.Fatal("el caso de 3 fallos ya excede el cap; ajusta la tabla")
	}
}

func TestExitoResetealContadorDeFallos(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "a@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", 0)
	addReadingEntry(t, db, userID, mangaID)

	// Primero un fallo, para dejar el contador en 1.
	client := &fakeClient{errFor: map[string]error{"md-1": errors.New("boom")}}
	w := newTestWorker(t, db, client)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce fallido: %v", err)
	}
	if fails, _ := sourceFailureState(t, db, mangaID); fails != 1 {
		t.Fatalf("preparación: esperaba 1 fallo, got %d", fails)
	}

	// Ahora una pasada exitosa sobre la misma fila.
	client.errFor = nil
	if err := w.pollSource(context.Background(), mangaID, client, "md-1"); err != nil {
		t.Fatalf("pollSource exitoso: %v", err)
	}

	fails, lastErr := sourceFailureState(t, db, mangaID)
	if fails != 0 {
		t.Errorf("el éxito no reseteó el contador: %d", fails)
	}
	if lastErr.Valid {
		t.Errorf("el éxito no limpió last_error: %q", lastErr.String)
	}
}

// TestRateLimitNoCuentaComoFallo: el rate limit es backpressure normal, no
// degradación. Ya tenía su propio reagendado corto; lo que verificamos es
// que no contamine el contador de fallos ni dispare el backoff largo.
func TestRateLimitNoCuentaComoFallo(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "a@example.com")
	mangaID := insertMatchedManga(t, db, "md-1", 0)
	addReadingEntry(t, db, userID, mangaID)

	client := &fakeClient{errFor: map[string]error{"md-1": releasesource.ErrRateLimited}}
	w := newTestWorker(t, db, client)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if fails, _ := sourceFailureState(t, db, mangaID); fails != 0 {
		t.Errorf("el rate limit incrementó consecutive_failures a %d", fails)
	}
	nextCheck, _ := nextCheckAt(t, db, mangaID)
	if !nextCheck.Valid {
		t.Fatal("el rate limit no reagendó la fila")
	}
	limite := time.Now().Add(RateLimitedDelay + time.Minute).UTC().Unix()
	if nextCheck.Int64 > limite {
		t.Errorf("el rate limit aplicó el backoff largo en vez de RateLimitedDelay")
	}
}
