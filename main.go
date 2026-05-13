// mokuji: tracker de manga público.
//
// Este binario abre la DB, corre migraciones, y arranca el servidor HTTP.
// Con el flag -migrate-only, aplica migraciones y sale sin levantar el
// servidor.
package main

import (
	"context"
	"database/sql"
	"embed"
	"flag"
	"io/fs"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/server"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func main() {
	migrateOnly := flag.Bool("migrate-only", false, "Aplicar migraciones y salir sin levantar servidor")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

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
	logger.Info("migraciones aplicadas", "cantidad", applied)

	if *migrateOnly {
		return
	}

	srv := server.New(server.Config{
		Addr:   envOr("HTTP_ADDR", ":8080"),
		DB:     db,
		Logger: logger,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("servidor terminó con error", "err", err)
			os.Exit(1)
		}
	case sig := <-sigCh:
		logger.Info("señal recibida, apagando", "señal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("error apagando servidor", "err", err)
			os.Exit(1)
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
