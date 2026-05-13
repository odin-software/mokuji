// mokuji: tracker de manga público.
//
// Este binario abre la DB, corre migraciones, levanta el servidor HTTP con el
// flow de autenticación, y limpia sesiones expiradas en background.
// Con el flag -migrate-only, aplica migraciones y sale.
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
	"strconv"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/auth"
	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/server"
	"git.odin.do/kengru/mokuji/internal/session"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed web/templates/layout.html web/templates/auth/*.html web/emails/*.txt
var assetsFS embed.FS

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

	migSrc, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		log.Fatalf("acceder migraciones embebidas: %v", err)
	}
	applied, err := migrations.Run(db, migSrc)
	if err != nil {
		log.Fatalf("aplicar migraciones: %v", err)
	}
	logger.Info("migraciones aplicadas", "cantidad", applied)

	if *migrateOnly {
		return
	}

	sessions := session.New(db, envBool("SECURE_COOKIES", false), logger)

	emailClient, err := email.New(email.Config{
		Host:     envOr("SMTP_HOST", "localhost"),
		Port:     envInt("SMTP_PORT", 1025),
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     envOr("SMTP_FROM", "mokuji <auth@mokuji.local>"),
		Logger:   logger,
	})
	if err != nil {
		log.Fatalf("configurar email: %v", err)
	}

	htmlFS, err := fs.Sub(assetsFS, "web/templates")
	if err != nil {
		log.Fatalf("acceder plantillas HTML: %v", err)
	}
	emailFS, err := fs.Sub(assetsFS, "web/emails")
	if err != nil {
		log.Fatalf("acceder plantillas de email: %v", err)
	}

	authHandler, err := auth.New(auth.Config{
		DB:             db,
		Sessions:       sessions,
		Email:          emailClient,
		HTMLTemplates:  htmlFS,
		EmailTemplates: emailFS,
		BaseURL:        envOr("BASE_URL", "http://localhost:8080"),
		Logger:         logger,
	})
	if err != nil {
		log.Fatalf("configurar auth: %v", err)
	}

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cleanupSessions(rootCtx, sessions, logger)

	srv := server.New(server.Config{
		Addr:     envOr("HTTP_ADDR", ":8080"),
		DB:       db,
		Logger:   logger,
		Sessions: sessions,
		Auth:     authHandler,
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
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("error apagando servidor", "err", err)
			os.Exit(1)
		}
	}
}

func cleanupSessions(ctx context.Context, m *session.Manager, logger *slog.Logger) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.CleanupExpired(ctx); err != nil {
				logger.Error("session cleanup", "err", err)
			}
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	switch v {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}
