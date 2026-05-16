package library

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.odin.do/kengru/mokuji/internal/releases/matcher"
)

// stubMatcher es un matcher por-source con respuestas predecibles para
// los tests del Backfiller. results es un map de title→Result; cualquier
// otro título cae al default. errs sobrescribe results.
type stubMatcher struct {
	results map[string]matcher.Result
	errs    map[string]error
	calls   []string
	source  string // solo para logging visual; no afecta lógica
}

func (s *stubMatcher) Match(_ context.Context, titles ...string) (matcher.Result, error) {
	t := ""
	if len(titles) > 0 {
		t = titles[0]
	}
	s.calls = append(s.calls, t)
	if err, ok := s.errs[t]; ok {
		return matcher.Result{}, err
	}
	if r, ok := s.results[t]; ok {
		return r, nil
	}
	return matcher.Result{Status: matcher.StatusNoResults}, nil
}

func newBackfiller(t *testing.T, db *sql.DB, matchers map[string]Matcher) *Backfiller {
	t.Helper()
	b, err := NewBackfiller(BackfillConfig{
		DB:             db,
		SourceMatchers: matchers,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func insertReadingEntry(t *testing.T, db *sql.DB, userID int64, title string) int64 {
	t.Helper()
	mangaSeq++
	res, err := db.Exec(
		`INSERT INTO manga (source, source_id, title, created_at, updated_at) VALUES ('anilist', ?, ?, 0, 0)`,
		"a-"+title, title)
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at) VALUES (?, ?, 'reading', 0, 0)`,
		userID, mid); err != nil {
		t.Fatal(err)
	}
	return mid
}

func TestBackfillerInsertsMissingSourceForReadingManga(t *testing.T) {
	db := setupDB(t)
	uid := insertUser(t, db, "alice@example.com")
	insertReadingEntry(t, db, uid, "Berserk")

	stub := &stubMatcher{results: map[string]matcher.Result{
		"Berserk": {Status: matcher.StatusMatched, SourceID: "asura-berserk"},
	}}
	b := newBackfiller(t, db, map[string]Matcher{"asura": stub})

	b.Run(context.Background())

	var status, sourceID string
	if err := db.QueryRow(`SELECT status, source_id FROM manga_sources WHERE source = 'asura'`).Scan(&status, &sourceID); err != nil {
		t.Fatal(err)
	}
	if status != "matched" || sourceID != "asura-berserk" {
		t.Errorf("fila: status=%q source_id=%q", status, sourceID)
	}
	if len(stub.calls) != 1 || stub.calls[0] != "Berserk" {
		t.Errorf("calls: %v", stub.calls)
	}
}

func TestBackfillerSkipsMangaWithExistingRow(t *testing.T) {
	db := setupDB(t)
	uid := insertUser(t, db, "alice@example.com")
	mid := insertReadingEntry(t, db, uid, "Berserk")
	// Ya hay fila — debería NO llamarse el matcher.
	if _, err := db.Exec(`INSERT INTO manga_sources (manga_id, source, source_id, status) VALUES (?, 'asura', 'existing', 'matched')`, mid); err != nil {
		t.Fatal(err)
	}

	stub := &stubMatcher{}
	b := newBackfiller(t, db, map[string]Matcher{"asura": stub})
	b.Run(context.Background())

	if len(stub.calls) != 0 {
		t.Errorf("no debería haber llamado al matcher: %v", stub.calls)
	}
}

func TestBackfillerSkipsNonReadingEntries(t *testing.T) {
	db := setupDB(t)
	uid := insertUser(t, db, "alice@example.com")
	mangaSeq++
	res, _ := db.Exec(`INSERT INTO manga (source, source_id, title, created_at, updated_at) VALUES ('anilist', ?, 'X', 0, 0)`, "a-x")
	mid, _ := res.LastInsertId()
	// status=completed → no se backfillea.
	if _, err := db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at) VALUES (?, ?, 'completed', 0, 0)`, uid, mid); err != nil {
		t.Fatal(err)
	}

	stub := &stubMatcher{}
	b := newBackfiller(t, db, map[string]Matcher{"asura": stub})
	b.Run(context.Background())

	if len(stub.calls) != 0 {
		t.Errorf("no debería pollar status=completed: %v", stub.calls)
	}
}

func TestBackfillerInsertsPlaceholderOnMatcherError(t *testing.T) {
	// Si el matcher falla (rate limit, red caída), insertamos placeholder
	// 'unmatched' para no quedar en loop infinito. El worker periódico o
	// el user pueden reintentar después.
	db := setupDB(t)
	uid := insertUser(t, db, "alice@example.com")
	insertReadingEntry(t, db, uid, "X")

	stub := &stubMatcher{errs: map[string]error{"X": errors.New("rate limited")}}
	b := newBackfiller(t, db, map[string]Matcher{"asura": stub})
	b.Run(context.Background())

	var status string
	if err := db.QueryRow(`SELECT status FROM manga_sources WHERE source = 'asura'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "unmatched" {
		t.Errorf("placeholder status: %q", status)
	}
}

func TestBackfillerHandlesMultipleSources(t *testing.T) {
	db := setupDB(t)
	uid := insertUser(t, db, "alice@example.com")
	insertReadingEntry(t, db, uid, "Berserk")

	mdStub := &stubMatcher{source: "mangadex", results: map[string]matcher.Result{
		"Berserk": {Status: matcher.StatusMatched, SourceID: "md-berserk"},
	}}
	asuraStub := &stubMatcher{source: "asura", results: map[string]matcher.Result{
		"Berserk": {Status: matcher.StatusNoResults},
	}}
	b := newBackfiller(t, db, map[string]Matcher{
		"mangadex": mdStub,
		"asura":    asuraStub,
	})
	b.Run(context.Background())

	// Debe haber 2 filas: mangadex matched + asura no_results.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM manga_sources`).Scan(&n)
	if n != 2 {
		t.Errorf("manga_sources rows: esperado 2, obtenido %d", n)
	}
}

func TestBackfillerStopsOnCancelledContext(t *testing.T) {
	db := setupDB(t)
	uid := insertUser(t, db, "alice@example.com")
	for range 5 {
		insertReadingEntry(t, db, uid, time.Now().Format("Manga-15:04:05.000000000"))
	}
	stub := &stubMatcher{}
	b := newBackfiller(t, db, map[string]Matcher{"asura": stub})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.Run(ctx) // debe retornar inmediato

	// Debería haber procesado 0 (canceled antes de empezar) o pocos.
	if len(stub.calls) > 5 {
		t.Errorf("calls tras cancel: %d (esperaba <=5)", len(stub.calls))
	}
}

func TestNewBackfillerRequiresMatchers(t *testing.T) {
	db := setupDB(t)
	if _, err := NewBackfiller(BackfillConfig{DB: db}); err == nil {
		t.Error("matchers vacío debería fallar")
	}
}
