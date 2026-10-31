package library

import (
	"database/sql"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/odin-software/mokuji/internal/migrations"
)

// migrationsUpTo copia de ../../migrations solo los archivos con prefijo
// numérico <= max, para dejar la DB justo antes de una migración.
func migrationsUpTo(t *testing.T, max string) fstest.MapFS {
	t.Helper()
	src := os.DirFS("../../migrations")
	entries, err := fs.ReadDir(src, ".")
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") || e.Name()[:4] > max {
			continue
		}
		b, err := fs.ReadFile(src, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = &fstest.MapFile{Data: b}
	}
	return out
}

func TestMigration0015(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrations.Run(db, migrationsUpTo(t, "0014")); err != nil {
		t.Fatalf("migrate hasta 0014: %v", err)
	}
	uid := insertUser(t, db, "alice@example.com")

	type row struct {
		lastCh string
		cursor sql.NullFloat64
		notes  sql.NullString
	}
	rows := []row{
		{"252", sql.NullFloat64{}, sql.NullString{}},
		{"10", sql.NullFloat64{Float64: 20, Valid: true}, sql.NullString{}},
		{"30", sql.NullFloat64{Float64: 20, Valid: true}, sql.NullString{}},
		{"vol 3", sql.NullFloat64{}, sql.NullString{String: "mis notas", Valid: true}},
		{"", sql.NullFloat64{}, sql.NullString{}},
	}
	var ids []int64
	for i, r := range rows {
		res, err := db.Exec(`INSERT INTO manga (source, source_id, title, created_at, updated_at) VALUES ('anilist', ?, 'x', 0, 0)`, i)
		if err != nil {
			t.Fatal(err)
		}
		mid, _ := res.LastInsertId()
		res, err = db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, last_chapter, last_read_chapter_number, notes, created_at, updated_at)
			VALUES (?, ?, 'reading', ?, ?, ?, ?, ?)`, uid, mid, r.lastCh, r.cursor, r.notes, time.Now().Unix(), time.Now().Unix())
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}

	if _, err := migrations.Run(db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("migrate completo: %v", err)
	}

	wantCursor := []sql.NullFloat64{{Float64: 252, Valid: true}, {Float64: 20, Valid: true}, {Float64: 30, Valid: true}, {}, {}}
	for i, id := range ids {
		var cursor sql.NullFloat64
		var notes sql.NullString
		if err := db.QueryRow(`SELECT last_read_chapter_number, notes FROM library_entries WHERE id = ?`, id).Scan(&cursor, &notes); err != nil {
			t.Fatal(err)
		}
		if cursor != wantCursor[i] {
			t.Errorf("fila %d (%q): cursor %v, esperado %v", i, rows[i].lastCh, cursor, wantCursor[i])
		}
		if i == 3 && notes.String != "progreso anterior: vol 3\nmis notas" {
			t.Errorf("notes de la fila no numérica: %q", notes.String)
		}
	}
	if _, err := db.Exec(`SELECT last_chapter FROM library_entries`); err == nil || !strings.Contains(err.Error(), "no such column") {
		t.Errorf("last_chapter sigue existiendo (err=%v)", err)
	}
}
