package migrations

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
)

func openDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func backups(t *testing.T, dbPath string) []string {
	t.Helper()
	m, err := filepath.Glob(dbPath + ".pre-migracion-*.bak")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRun_BaseNuevaNoRespalda(t *testing.T) {
	db, path := openDB(t)
	n, err := Run(db, fstest.MapFS{"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER);`)}})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if b := backups(t, path); len(b) != 0 {
		t.Errorf("base nueva no debería respaldarse: %v", b)
	}
}

func TestRun_SinPendientesNoRespalda(t *testing.T) {
	db, path := openDB(t)
	src := fstest.MapFS{"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER);`)}}
	if _, err := Run(db, src); err != nil {
		t.Fatal(err)
	}
	if n, err := Run(db, src); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if b := backups(t, path); len(b) != 0 {
		t.Errorf("sin pendientes no debería respaldarse: %v", b)
	}
}

func TestRun_RespaldaAntesDeMigrarUnaBaseConDatos(t *testing.T) {
	db, path := openDB(t)
	if _, err := Run(db, fstest.MapFS{"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER, x TEXT); INSERT INTO a VALUES (1, 'viejo');`)}}); err != nil {
		t.Fatal(err)
	}
	src := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER, x TEXT);`)},
		"0002_b.sql": {Data: []byte(`ALTER TABLE a DROP COLUMN x;`)},
	}
	if n, err := Run(db, src); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	b := backups(t, path)
	if len(b) != 1 {
		t.Fatalf("esperaba 1 respaldo, hay %v", b)
	}
	if fi, _ := os.Stat(b[0]); fi.Mode().Perm() != 0o600 {
		t.Errorf("permisos = %v, esperado 0600", fi.Mode().Perm())
	}
	// El respaldo conserva la columna que la migración borró.
	bdb, err := sql.Open("sqlite", b[0])
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()
	var x string
	if err := bdb.QueryRow(`SELECT x FROM a WHERE id = 1`).Scan(&x); err != nil || x != "viejo" {
		t.Errorf("respaldo sin el dato original: x=%q err=%v", x, err)
	}
}

func TestRun_MigracionQueFallaDejaLaBaseIntactaYElRespaldo(t *testing.T) {
	db, path := openDB(t)
	if _, err := Run(db, fstest.MapFS{"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER, x TEXT); INSERT INTO a VALUES (1, 'viejo');`)}}); err != nil {
		t.Fatal(err)
	}
	src := fstest.MapFS{
		"0001_a.sql":    {Data: []byte(`x`)},
		"0002_mala.sql": {Data: []byte(`ALTER TABLE a DROP COLUMN x; SELECT * FROM no_existe;`)},
	}
	if _, err := Run(db, src); err == nil {
		t.Fatal("esperaba error")
	}
	var x string
	if err := db.QueryRow(`SELECT x FROM a WHERE id = 1`).Scan(&x); err != nil || x != "viejo" {
		t.Errorf("la base debería quedar intacta (rollback): x=%q err=%v", x, err)
	}
	var v string
	if err := db.QueryRow(`SELECT version FROM schema_migrations WHERE version = '0002_mala.sql'`).Scan(&v); err != sql.ErrNoRows {
		t.Errorf("la migración fallida no debe quedar registrada: %v", err)
	}
	if len(backups(t, path)) != 1 {
		t.Errorf("el respaldo debe existir aunque la migración falle")
	}
}

func TestRun_ConservaSoloLosUltimosRespaldos(t *testing.T) {
	db, path := openDB(t)
	if _, err := Run(db, fstest.MapFS{"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER);`)}}); err != nil {
		t.Fatal(err)
	}
	for _, ts := range []string{"20200101-000001", "20200101-000002", "20200101-000003", "20200101-000004"} {
		if err := os.WriteFile(path+".pre-migracion-"+ts+".bak", []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`x`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE b (id INTEGER);`)},
	}
	if _, err := Run(db, src); err != nil {
		t.Fatal(err)
	}
	if b := backups(t, path); len(b) != keepBackups {
		t.Errorf("esperaba %d respaldos, hay %d: %v", keepBackups, len(b), b)
	}
}
