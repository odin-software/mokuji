// mokuji: tracker de manga público.
//
// Este binario en su estado actual solo aplica migraciones y sale. El
// servidor HTTP se agrega en commits siguientes.
package main

import (
	"database/sql"
	"embed"
	"flag"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/migrations"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func main() {
	migrateOnly := flag.Bool("migrate-only", false, "Aplicar migraciones y salir sin levantar servidor")
	flag.Parse()

	dbPath := envOr("DB_PATH", "./data/mokuji.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("crear directorio de DB: %v", err)
	}

	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatalf("abrir DB: %v", err)
	}
	defer db.Close()

	src, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		log.Fatalf("acceder migraciones embebidas: %v", err)
	}

	applied, err := migrations.Run(db, src)
	if err != nil {
		log.Fatalf("aplicar migraciones: %v", err)
	}
	log.Printf("migraciones aplicadas: %d", applied)

	if *migrateOnly {
		return
	}

	log.Print("nada más que hacer por ahora — el servidor HTTP llega en el próximo commit")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
