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

	"golang.org/x/time/rate"

	"git.odin.do/kengru/mokuji/internal/admin"
	"git.odin.do/kengru/mokuji/internal/auth"
	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/home"
	"git.odin.do/kengru/mokuji/internal/library"
	"git.odin.do/kengru/mokuji/internal/mangadex"
	"git.odin.do/kengru/mokuji/internal/mangasource/anilist"
	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/profile"
	"git.odin.do/kengru/mokuji/internal/ratelimit"
	"git.odin.do/kengru/mokuji/internal/releases/digest"
	releaseshandler "git.odin.do/kengru/mokuji/internal/releases/handler"
	"git.odin.do/kengru/mokuji/internal/releases/matcher"
	"git.odin.do/kengru/mokuji/internal/releases/worker"
	"git.odin.do/kengru/mokuji/internal/server"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/unsubscribe"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed web
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
	webFS, err := fs.Sub(assetsFS, "web")
	if err != nil {
		log.Fatalf("acceder assets de web: %v", err)
	}

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Rate limiters para auth.
	//   /auth/start por IP:    ~5/min sostenido, burst 5
	//   /auth/start por email: ~3/hora sostenido, burst 3
	//   /auth/verify por IP:   ~10/min sostenido, burst 10
	startIPLim := ratelimit.New(rate.Every(12*time.Second), 5)
	startEmailLim := ratelimit.New(rate.Every(20*time.Minute), 3)
	verifyIPLim := ratelimit.New(rate.Every(6*time.Second), 10)
	go startIPLim.Cleanup(rootCtx, 1*time.Hour)
	go startEmailLim.Cleanup(rootCtx, 2*time.Hour)
	go verifyIPLim.Cleanup(rootCtx, 30*time.Minute)

	authHandler, err := auth.New(auth.Config{
		DB:                db,
		Sessions:          sessions,
		Email:             emailClient,
		HTMLTemplates:     htmlFS,
		EmailTemplates:    emailFS,
		BaseURL:           envOr("BASE_URL", "http://localhost:8080"),
		Logger:            logger,
		TrustProxy:        envBool("TRUST_PROXY", false),
		StartIPLimiter:    startIPLim,
		StartEmailLimiter: startEmailLim,
		VerifyIPLimiter:   verifyIPLim,
	})
	if err != nil {
		log.Fatalf("configurar auth: %v", err)
	}

	homeHandler, err := home.New(home.Config{
		DB:            db,
		HTMLTemplates: htmlFS,
		Logger:        logger,
	})
	if err != nil {
		log.Fatalf("configurar home: %v", err)
	}

	// Cliente MangaDex + matcher: alimentan la columna manga.mangadex_id al
	// agregar un manga a la library (matching híbrido, ADR 0013 §3).
	mangadexClient := mangadex.New()
	releaseMatcher := matcher.New(mangadexClient)

	libraryHandler, err := library.New(library.Config{
		DB:            db,
		MangaSource:   anilist.New(),
		Matcher:       releaseMatcher,
		HTMLTemplates: htmlFS,
		Logger:        logger,
	})
	if err != nil {
		log.Fatalf("configurar library: %v", err)
	}

	profileHandler, err := profile.New(profile.Config{
		DB:            db,
		HTMLTemplates: htmlFS,
		Logger:        logger,
	})
	if err != nil {
		log.Fatalf("configurar profile: %v", err)
	}

	releasesHandler, err := releaseshandler.New(releaseshandler.Config{
		DB:            db,
		HTMLTemplates: htmlFS,
		Logger:        logger,
	})
	if err != nil {
		log.Fatalf("configurar releases: %v", err)
	}

	// Signer de tokens HMAC para opt-out de emails (ADR 0013 §6, RFC 8058).
	// El secret es server-side; rotarlo invalida todos los links de baja
	// pendientes. En dev usamos default fijo si no hay env var, en prod
	// fallamos rápido para evitar firmas con secret nulo.
	unsubSecret := os.Getenv("UNSUBSCRIBE_SECRET")
	if unsubSecret == "" {
		if envBool("SECURE_COOKIES", false) {
			log.Fatal("UNSUBSCRIBE_SECRET requerido en producción")
		}
		unsubSecret = "dev-unsubscribe-secret-no-uses-en-produccion-32b!!"
		logger.Warn("UNSUBSCRIBE_SECRET no configurado, usando default de dev")
	}
	unsubSigner, err := unsubscribe.NewSigner([]byte(unsubSecret))
	if err != nil {
		log.Fatalf("configurar unsubscribe signer: %v", err)
	}
	unsubHandler, err := unsubscribe.New(unsubscribe.Config{
		DB:            db,
		Signer:        unsubSigner,
		HTMLTemplates: htmlFS,
		Logger:        logger,
	})
	if err != nil {
		log.Fatalf("configurar unsubscribe: %v", err)
	}

	// Digest semanal: Sender (compartido entre worker y endpoint admin) +
	// worker goroutine que dispara domingos 9am UTC (ADR 0013 §6).
	digestSender, err := digest.New(digest.Config{
		DB:      db,
		Email:   emailClient,
		Signer:  unsubSigner,
		Emails:  emailFS,
		BaseURL: envOr("BASE_URL", "http://localhost:8080"),
		Logger:  logger,
	})
	if err != nil {
		log.Fatalf("configurar digest sender: %v", err)
	}
	digestWorker, err := digest.NewWorker(digest.WorkerConfig{
		DB:     db,
		Sender: digestSender,
		Logger: logger,
	})
	if err != nil {
		log.Fatalf("configurar digest worker: %v", err)
	}
	go digestWorker.Run(rootCtx)

	// Endpoint admin para disparar digests manualmente (test/QA). Si
	// ADMIN_TOKEN está vacío, el endpoint responde 401 a todo — feature
	// "off" sin código condicional adicional.
	adminHandler, err := admin.New(admin.Config{
		DB:     db,
		Token:  os.Getenv("ADMIN_TOKEN"),
		Digest: digestSender,
		Logger: logger,
	})
	if err != nil {
		log.Fatalf("configurar admin: %v", err)
	}

	go cleanupSessions(rootCtx, sessions, logger)

	// Worker periódico que polleea MangaDex (ADR 0013 §5). Reusa el mismo
	// cliente que el matcher — el rate limiter cliente-side garantiza que
	// los dos callers se mantengan bajo el techo de MangaDex.
	releasesWorker, err := worker.New(worker.Config{
		DB:     db,
		Client: mangadexClient,
		Logger: logger,
	})
	if err != nil {
		log.Fatalf("configurar release worker: %v", err)
	}
	go releasesWorker.Run(rootCtx)

	srv := server.New(server.Config{
		Addr:         envOr("HTTP_ADDR", ":8080"),
		DB:           db,
		Logger:       logger,
		Sessions:     sessions,
		Auth:         authHandler,
		Home:         homeHandler,
		Library:      libraryHandler,
		Profile:      profileHandler,
		Releases:     releasesHandler,
		Unsubscribe:  unsubHandler,
		Admin:        adminHandler,
		StaticAssets: webFS,
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
