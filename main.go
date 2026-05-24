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
	"git.odin.do/kengru/mokuji/internal/asura"
	"git.odin.do/kengru/mokuji/internal/auth"
	"git.odin.do/kengru/mokuji/internal/comick"
	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/home"
	"git.odin.do/kengru/mokuji/internal/library"
	"git.odin.do/kengru/mokuji/internal/mangadex"
	"git.odin.do/kengru/mokuji/internal/flash"
	"git.odin.do/kengru/mokuji/internal/mangasource/anilist"
	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/profile"
	"git.odin.do/kengru/mokuji/internal/ratelimit"
	"git.odin.do/kengru/mokuji/internal/releases/digest"
	releaseshandler "git.odin.do/kengru/mokuji/internal/releases/handler"
	"git.odin.do/kengru/mokuji/internal/releases/matcher"
	"git.odin.do/kengru/mokuji/internal/releases/worker"
	"git.odin.do/kengru/mokuji/internal/releasesource"
	"git.odin.do/kengru/mokuji/internal/server"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/unsubscribe"
	"git.odin.do/kengru/mokuji/internal/version"
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

	// Rate limiters para auth + unsubscribe.
	//   /auth/start por IP:    ~5/min sostenido, burst 5
	//   /auth/start por email: ~3/hora sostenido, burst 3
	//   /auth/verify por IP:   ~10/min sostenido, burst 10
	//   /unsubscribe por IP:   ~60/min sostenido, burst 20 (más permisivo
	//                          porque users legítimos pueden refrescar y
	//                          re-clickear; un fuzzer enumerando tokens
	//                          HMAC se topa con el cap igual).
	startIPLim := ratelimit.New(rate.Every(12*time.Second), 5)
	startEmailLim := ratelimit.New(rate.Every(20*time.Minute), 3)
	verifyIPLim := ratelimit.New(rate.Every(6*time.Second), 10)
	unsubIPLim := ratelimit.New(rate.Every(time.Second), 20)
	go startIPLim.Cleanup(rootCtx, 1*time.Hour)
	go startEmailLim.Cleanup(rootCtx, 2*time.Hour)
	go verifyIPLim.Cleanup(rootCtx, 30*time.Minute)
	go unsubIPLim.Cleanup(rootCtx, 30*time.Minute)

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

	// Multi-source release tracking (ADR 0014). Cada source es un
	// releasesource.Client; main wirea el map por nombre. MangaDex
	// siempre on; Asura es opt-in via ASURA_ENABLED (default on en
	// mokuji.org; self-hosters conservadores la apagan). Comick queda
	// fuera de v1 por Cloudflare managed challenge — no se puede hablar
	// con su API desde Go vanilla.
	mangadexClient := mangadex.New()
	sourceClients := map[string]releasesource.Client{
		mangadexClient.Name(): mangadexClient,
	}
	sourceMatchers := map[string]library.Matcher{
		mangadexClient.Name(): matcher.New(mangadexClient),
	}
	if envBool("ASURA_ENABLED", true) {
		opts := []asura.Option{
			asura.WithUserAgent("mokuji/" + version.Value + " (+" + envOr("BASE_URL", "https://mokuji.org") + ")"),
		}
		if u := os.Getenv("ASURA_API_BASE_URL"); u != "" {
			opts = append(opts, asura.WithAPIBaseURL(u))
		}
		if u := os.Getenv("ASURA_FRONTEND_BASE_URL"); u != "" {
			opts = append(opts, asura.WithFrontendBaseURL(u))
		}
		asuraClient := asura.New(opts...)
		sourceClients[asuraClient.Name()] = asuraClient
		sourceMatchers[asuraClient.Name()] = matcher.New(asuraClient)
		logger.Info("source asura habilitada", "api_base", envOr("ASURA_API_BASE_URL", "default"))
	}
	if envBool("COMICK_ENABLED", true) {
		opts := []comick.Option{
			comick.WithUserAgent("mokuji/" + version.Value + " (+" + envOr("BASE_URL", "https://mokuji.org") + ")"),
		}
		if u := os.Getenv("COMICK_API_BASE_URL"); u != "" {
			opts = append(opts, comick.WithAPIBaseURL(u))
		}
		if u := os.Getenv("COMICK_FRONTEND_BASE_URL"); u != "" {
			opts = append(opts, comick.WithFrontendBaseURL(u))
		}
		comickClient := comick.New(opts...)
		sourceClients[comickClient.Name()] = comickClient
		sourceMatchers[comickClient.Name()] = matcher.New(comickClient)
		logger.Info("source comick habilitada", "api_base", envOr("COMICK_API_BASE_URL", "default"))
	}

	// Worker periódico que polleea cada source (ADR 0014 §5). dispatch
	// vía Clients[source] según la fila de manga_sources.
	releasesWorker, err := worker.New(worker.Config{
		DB:      db,
		Clients: sourceClients,
		Logger:  logger,
	})
	if err != nil {
		log.Fatalf("configurar release worker: %v", err)
	}

	// Signer de tokens HMAC para opt-out de emails (ADR 0013 §6, RFC 8058)
	// y para el shortcut /r/{token} del digest. El secret es server-side;
	// rotarlo invalida todos los tokens previos. En dev usamos default fijo
	// si no hay env var, en prod fallamos rápido para evitar firmas con
	// secret nulo. Se construye acá (antes del library handler) porque
	// library lo necesita para montar /r/{token}.
	unsubSecret := os.Getenv("UNSUBSCRIBE_SECRET")
	if unsubSecret == "" {
		if envBool("SECURE_COOKIES", false) {
			log.Fatal("UNSUBSCRIBE_SECRET requerido en producción")
		}
		unsubSecret = "dev-unsubscribe-secret-no-usar-en-produccion!"
		logger.Warn("UNSUBSCRIBE_SECRET no configurado, usando default de dev")
	}
	unsubSigner, err := unsubscribe.NewSigner([]byte(unsubSecret))
	if err != nil {
		log.Fatalf("configurar unsubscribe signer: %v", err)
	}

	libraryHandler, err := library.New(library.Config{
		DB:             db,
		MangaSource:    anilist.New(),
		SourceMatchers: sourceMatchers,
		// Poller fire-and-forget: library.add lanza PollOne en goroutine
		// con BackgroundCtx (no el del request) — mangas grandes como
		// One Piece tardan minutos en paginar todos los caps; sin async,
		// el user vería la request colgada hasta que termine el poll.
		Poller:        releasesWorker,
		BackgroundCtx: rootCtx,
		Signer:        unsubSigner,
		HTMLTemplates: htmlFS,
		Logger:        logger,
	})
	if err != nil {
		log.Fatalf("configurar library: %v", err)
	}

	// Flasher: cookie one-shot para feedback post-redirect. Stateless.
	// Comparte el flag SECURE_COOKIES con sessions — en prod (HTTPS)
	// las cookies viajan con Secure, en dev local sin TLS no.
	flasher := flash.New(envBool("SECURE_COOKIES", false))

	profileHandler, err := profile.New(profile.Config{
		DB:             db,
		HTMLTemplates:  htmlFS,
		EmailTemplates: emailFS,
		Email:          emailClient,
		BaseURL:        envOr("BASE_URL", "http://localhost:8080"),
		Flasher:        flasher,
		Logger:         logger,
	})
	if err != nil {
		log.Fatalf("configurar profile: %v", err)
	}

	// Pepper para el audit log de borrado de cuentas (deletion_log). Si no
	// está seteado, los hashes son enumerables: aceptable en dev, no en prod.
	pepper := os.Getenv("DELETION_LOG_PEPPER")
	if pepper == "" && envBool("SECURE_COOKIES", false) {
		log.Fatal("DELETION_LOG_PEPPER requerido en producción")
	}
	profile.SetDeletionLogPepper(pepper)

	// Worker diario que hace hard delete de cuentas con deleted_at +
	// GracePeriod < now (ADR 0013 §11.2).
	cleanupWorker, err := profile.NewCleanupWorker(profile.CleanupWorkerConfig{
		DB:     db,
		Pepper: pepper,
		Logger: logger,
	})
	if err != nil {
		log.Fatalf("configurar cleanup worker: %v", err)
	}
	go cleanupWorker.Run(rootCtx)

	releasesHandler, err := releaseshandler.New(releaseshandler.Config{
		DB:            db,
		HTMLTemplates: htmlFS,
		Logger:        logger,
	})
	if err != nil {
		log.Fatalf("configurar releases: %v", err)
	}

	// home se construye después de releasesHandler porque el widget de
	// "últimos releases" en / reusa el método UnreadReleases. El orden
	// importa solo para wiring; no hay ciclo de import (home → releases).
	homeHandler, err := home.New(home.Config{
		DB:            db,
		HTMLTemplates: htmlFS,
		Releases:      releasesHandler,
		Logger:        logger,
	})
	if err != nil {
		log.Fatalf("configurar home: %v", err)
	}

	unsubHandler, err := unsubscribe.New(unsubscribe.Config{
		DB:            db,
		Signer:        unsubSigner,
		HTMLTemplates: htmlFS,
		IPLimiter:     unsubIPLim,
		TrustProxy:    envBool("TRUST_PROXY", false),
		Logger:        logger,
	})
	if err != nil {
		log.Fatalf("configurar unsubscribe: %v", err)
	}

	// Digest semanal: Sender (compartido entre worker y endpoint admin) +
	// worker goroutine que dispara domingos 9am UTC (ADR 0013 §6).
	// SMTP_FROM_PRODUCT separa product emails (digest) del From de auth
	// (OTP). Mantenerlos distintos protege deliverability cruzada: un user
	// que silencia el digest no pierde OTPs, y un report de spam contra
	// uno no degrada al otro. Vacío = usa SMTP_FROM (comportamiento previo).
	digestSender, err := digest.New(digest.Config{
		DB:          db,
		Email:       emailClient,
		Signer:      unsubSigner,
		Emails:      emailFS,
		BaseURL:     envOr("BASE_URL", "http://localhost:8080"),
		FromAddress: os.Getenv("SMTP_FROM_PRODUCT"),
		Logger:      logger,
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
		Health: releasesWorker,
		Logger: logger,
	})
	if err != nil {
		log.Fatalf("configurar admin: %v", err)
	}

	// Backfill multi-source: al arrancar, busca mangas en biblioteca que
	// no tienen fila en manga_sources para alguna source configurada y
	// les corre el matcher. Fire-and-forget; el rate limiter de cada
	// cliente HTTP regula la velocidad. Si nadie habilitó sources
	// nuevas desde el último arranque, el query devuelve cero filas y
	// el job termina rápido sin red.
	backfiller, err := library.NewBackfiller(library.BackfillConfig{
		DB:             db,
		SourceMatchers: sourceMatchers,
		Logger:         logger,
	})
	if err != nil {
		log.Fatalf("configurar backfiller: %v", err)
	}
	go backfiller.Run(rootCtx)

	go cleanupSessions(rootCtx, sessions, logger)
	go releasesWorker.Run(rootCtx)

	srv := server.New(server.Config{
		Addr:          envOr("HTTP_ADDR", ":8080"),
		DB:            db,
		Logger:        logger,
		Sessions:      sessions,
		Auth:          authHandler,
		Home:          homeHandler,
		Library:       libraryHandler,
		Profile:       profileHandler,
		Releases:      releasesHandler,
		Unsubscribe:   unsubHandler,
		Admin:         adminHandler,
		StaticAssets:  webFS,
		HTMLTemplates: htmlFS,
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
