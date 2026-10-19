package library

import (
	"context"
	"database/sql"
	"reflect"
	"strconv"
	"testing"
	"time"

	"git.odin.do/kengru/mokuji/internal/malimport"
	"git.odin.do/kengru/mokuji/internal/mangasource"
)

// foundFor arma el map que devolvería AniList: MAL id → manga con
// AniList id 30000 + MAL id.
func foundFor(malIDs ...int) map[int]mangasource.SearchResult {
	out := map[int]mangasource.SearchResult{}
	for _, id := range malIDs {
		out[id] = mangasource.SearchResult{
			SourceID: itoa(30000 + id),
			Title:    "manga " + itoa(id),
		}
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

type entryRow struct {
	Status   string
	Cursor   sql.NullFloat64
	Notes    sql.NullString
	Favorite int
}

func loadEntryRow(t *testing.T, db *sql.DB, userID int64, anilistID string) (entryRow, bool) {
	t.Helper()
	var r entryRow
	err := db.QueryRow(`
		SELECT le.status, le.last_read_chapter_number, le.notes, le.is_favorite
		FROM library_entries le JOIN manga m ON m.id = le.manga_id
		WHERE le.user_id = ? AND m.source_id = ?`, userID, anilistID,
	).Scan(&r.Status, &r.Cursor, &r.Notes, &r.Favorite)
	if err == sql.ErrNoRows {
		return r, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

// insertExisting crea una entry previa del user para el manga de AniList
// 30000+malID, con el estado, cursor, notas y favorito dados.
func insertExisting(t *testing.T, h *Handler, db *sql.DB, userID int64, malID int, status string, cursor float64, notes string, fav bool) {
	t.Helper()
	mangaID, err := h.upsertManga(context.Background(), h.src.Name(), itoa(30000+malID), "previo", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO library_entries (user_id, manga_id, status, last_read_chapter_number, notes, is_favorite, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, mangaID, status, cursor, notes, boolToInt(fav), time.Now().Unix(), time.Now().Unix(),
	); err != nil {
		t.Fatal(err)
	}
}

func countEntries(t *testing.T, db *sql.DB, userID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_entries WHERE user_id = ?`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImportCreatesEntriesWithStatusAndCursor(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	user := insertUser(t, db, "alice@example.com")

	entries := []malimport.Entry{
		{MALID: 2, Title: "Berserk", ReadChapters: 380, Status: "reading"},
		{MALID: 13, Title: "One Piece", ReadChapters: 0, Status: "planned"},
		{MALID: 1706, Title: "SBR", ReadChapters: 95, Status: "completed"},
	}
	sum, _, err := h.applyMALImport(context.Background(), user, entries, foundFor(2, 13, 1706))
	if err != nil {
		t.Fatalf("applyMALImport: %v", err)
	}
	if sum.Reading != 1 || sum.Planned != 1 || sum.Completed != 1 {
		t.Errorf("resumen: %+v", sum)
	}

	berserk, ok := loadEntryRow(t, db, user, "30002")
	if !ok || berserk.Status != "reading" || berserk.Cursor.Float64 != 380 {
		t.Errorf("Berserk: %+v (existe=%v)", berserk, ok)
	}
	onePiece, _ := loadEntryRow(t, db, user, "30013")
	if onePiece.Status != "planned" || onePiece.Cursor.Valid {
		t.Errorf("One Piece: esperado planned con cursor NULL, obtenido %+v", onePiece)
	}
	sbr, _ := loadEntryRow(t, db, user, "31706")
	if sbr.Status != "completed" || sbr.Cursor.Float64 != 95 {
		t.Errorf("SBR: %+v", sbr)
	}
}

func TestImportExistingOnlyRaisesCursor(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	user := insertUser(t, db, "alice@example.com")
	insertExisting(t, h, db, user, 2, "completed", 50, "x", true)

	entries := []malimport.Entry{{MALID: 2, Title: "Berserk", ReadChapters: 80, Status: "reading"}}
	sum, newReading, err := h.applyMALImport(context.Background(), user, entries, foundFor(2))
	if err != nil {
		t.Fatalf("applyMALImport: %v", err)
	}
	if sum.Updated != 1 || sum.Reading != 0 {
		t.Errorf("resumen: %+v", sum)
	}
	if len(newReading) != 0 {
		t.Errorf("una entry existente no es nueva para matchear: %+v", newReading)
	}
	got, _ := loadEntryRow(t, db, user, "30002")
	want := entryRow{Status: "completed", Cursor: sql.NullFloat64{Float64: 80, Valid: true}, Notes: sql.NullString{String: "x", Valid: true}, Favorite: 1}
	if got != want {
		t.Errorf("entry:\n got  %+v\n want %+v", got, want)
	}
}

func TestImportExistingNeverLowersCursor(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	user := insertUser(t, db, "alice@example.com")
	insertExisting(t, h, db, user, 2, "reading", 50, "", false)

	entries := []malimport.Entry{{MALID: 2, Title: "Berserk", ReadChapters: 10, Status: "reading"}}
	sum, _, err := h.applyMALImport(context.Background(), user, entries, foundFor(2))
	if err != nil {
		t.Fatalf("applyMALImport: %v", err)
	}
	if sum.Unchanged != 1 || sum.Updated != 0 {
		t.Errorf("resumen: %+v", sum)
	}
	if got, _ := loadEntryRow(t, db, user, "30002"); got.Cursor.Float64 != 50 {
		t.Errorf("cursor: esperado 50, obtenido %v", got.Cursor)
	}
}

func TestImportDroppedSkippedAndExistingUntouched(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	user := insertUser(t, db, "alice@example.com")
	insertExisting(t, h, db, user, 2, "reading", 5, "", false)

	entries := []malimport.Entry{
		{MALID: 2, Title: "Berserk", ReadChapters: 300, Status: ""},
		{MALID: 11, Title: "Naruto", ReadChapters: 40, Status: ""},
	}
	sum, _, err := h.applyMALImport(context.Background(), user, entries, foundFor(2, 11))
	if err != nil {
		t.Fatalf("applyMALImport: %v", err)
	}
	if sum.Dropped != 2 {
		t.Errorf("Dropped: esperado 2, obtenido %d", sum.Dropped)
	}
	if _, ok := loadEntryRow(t, db, user, "30011"); ok {
		t.Error("Naruto (Dropped) no debería importarse")
	}
	if got, _ := loadEntryRow(t, db, user, "30002"); got.Cursor.Float64 != 5 {
		t.Errorf("Berserk existente no debería cambiar: %+v", got)
	}
}

func TestImportNotFoundListed(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	user := insertUser(t, db, "alice@example.com")

	entries := []malimport.Entry{
		{MALID: 2, Title: "Berserk", Status: "reading"},
		{MALID: 99, Title: "Doujin raro", Status: "reading"},
		{MALID: 98, Title: "Otro raro", Status: "planned"},
	}
	sum, _, err := h.applyMALImport(context.Background(), user, entries, foundFor(2))
	if err != nil {
		t.Fatalf("applyMALImport: %v", err)
	}
	if want := []string{"Doujin raro", "Otro raro"}; !reflect.DeepEqual(sum.NotFound, want) {
		t.Errorf("NotFound: esperado %v, obtenido %v", want, sum.NotFound)
	}
	if n := countEntries(t, db, user); n != 1 {
		t.Errorf("entries: esperado 1, obtenido %d", n)
	}
}

func TestImportIsIdempotent(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	user := insertUser(t, db, "alice@example.com")

	entries := []malimport.Entry{
		{MALID: 2, Title: "Berserk", ReadChapters: 380, Status: "reading"},
		{MALID: 13, Title: "One Piece", Status: "planned"},
	}
	ctx := context.Background()
	if _, _, err := h.applyMALImport(ctx, user, entries, foundFor(2, 13)); err != nil {
		t.Fatal(err)
	}
	sum, newReading, err := h.applyMALImport(ctx, user, entries, foundFor(2, 13))
	if err != nil {
		t.Fatal(err)
	}
	want := ImportSummary{Unchanged: 2}
	if !reflect.DeepEqual(sum, want) {
		t.Errorf("segunda corrida:\n got  %+v\n want %+v", sum, want)
	}
	if len(newReading) != 0 {
		t.Errorf("segunda corrida no debería tener nada nuevo para matchear: %+v", newReading)
	}
	if n := countEntries(t, db, user); n != 2 {
		t.Errorf("entries: esperado 2, obtenido %d", n)
	}
}

func TestImportReturnsOnlyNewReadingForMatching(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	user := insertUser(t, db, "alice@example.com")

	entries := []malimport.Entry{
		{MALID: 2, Title: "Berserk", Status: "reading"},
		{MALID: 13, Title: "One Piece", Status: "planned"},
		{MALID: 1706, Title: "SBR", Status: "completed"},
	}
	_, newReading, err := h.applyMALImport(context.Background(), user, entries, foundFor(2, 13, 1706))
	if err != nil {
		t.Fatal(err)
	}
	if len(newReading) != 1 || newReading[0].Title != "manga 2" || newReading[0].MangaID == 0 {
		t.Errorf("newReading: %+v", newReading)
	}
}
