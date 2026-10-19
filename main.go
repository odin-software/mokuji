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
	"git.odin.do/kengru/mokuji/internal/errrender"
	"git.odin.do/kengru/mokuji/internal/feedback"
	"git.odin.do/kengru/mokuji/internal/flash"
	"git.odin.do/kengru/mokuji/internal/grant"
	"git.odin.do/kengru/mokuji/internal/home"
	"git.odin.do/kengru/mokuji/internal/library"
	"git.odin.do/kengru/mokuji/internal/mangadex"
	"git.odin.do/kengru/mokuji/internal/mangasource/anilist"
	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/onboarding"
	"git.odin.do/kengru/mokuji/internal/profile"
	"git.odin.do/kengru/mokuji/internal/ratelimit"
	"git.odin.do/kengru/mokuji/internal/releases/digest"
	releaseshandler "git.odin.do/kengru/mokuji/internal/releases/handler"
	"git.odin.do/kengru/mokuji/internal/releases/matcher"
	"git.odin.do/kengru/mokuji/internal/releases/worker"
	"git.odin.do/kengru/mokuji/internal/releasesource"
	"git.odin.do/kengru/mokuji/internal/server"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/turnstile"
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

	// ErrorRenderer: construido antes que los handlers para que cada uno lo
	// reciba como dependencia. Sirve 404 y 500 estilizados con el mismo
	// layout/CSS del resto de la app. Si falla (template roto), seguimos
	// arrancando — los handlers caen al texto plano de net/http.
	errs, err := errrender.New(htmlFS, logger)
	if err != nil {
		logger.Error("inicializar error renderer", "err", err)
		// errs queda nil; los handlers operan con fallback plano.
	}

	// Rate limiters para auth + unsubscribe.
	//   /auth/start por IP:     ~5/min sostenido, burst 3
	//   /auth/start por email:  ~3/hora sostenido, burst 3
	//   /auth/start conocidos/desconocidos: presupuesto de envío partido por
	//                           destinatario.
	//                             conocidos:    ~3/hora sostenido, burst 10
	//                                           -> techo ~72/día en régimen
	//                             desconocidos: ~1/hora sostenido, burst 3
	//                                           -> techo ~24/día en régimen
	//                           Los ~96/día de arriba son solo el sostenido
	//                           de OTP, no un techo real: los burst (10+3)
	//                           se sirven aparte de esa cuenta y se
	//                           rellenan en cada restart del proceso — o
	//                           sea, en cada deploy del CD —, así que las
	//                           primeras 24h tras un start pueden llegar a
	//                           ~109. Tampoco cuentan el digest semanal ni
	//                           el form de feedback: mandan por la misma
	//                           cuenta y la misma quota del proveedor, sin
	//                           límite propio acá. Lo que importa no es el
	//                           total sino el aislamiento: un flood de
	//                           direcciones random no puede tocar el
	//                           presupuesto de login de la gente que ya
	//                           tiene cuenta.
	//   /auth/verify por IP:    ~10/min sostenido, burst 10
	//   /unsubscribe por IP:    ~60/min sostenido, burst 20 (más permisivo
	//                           porque users legítimos pueden refrescar y
	//                           re-clickear; un fuzzer enumerando tokens
	//                           HMAC se topa con el cap igual).
	startIPLim := ratelimit.New(rate.Every(12*time.Second), 3)
	startEmailLim := ratelimit.New(rate.Every(20*time.Minute), 3)
	startKnownLim := ratelimit.New(rate.Every(20*time.Minute), 10)
	startUnknownLim := ratelimit.New(rate.Every(60*time.Minute), 3)
	verifyIPLim := ratelimit.New(rate.Every(6*time.Second), 10)
	unsubIPLim := ratelimit.New(rate.Every(time.Second), 20)
	grantLim := ratelimit.New(rate.Every(time.Minute), 5)
	go startIPLim.Cleanup(rootCtx, 1*time.Hour)
	go startEmailLim.Cleanup(rootCtx, 2*time.Hour)
	go startKnownLim.Cleanup(rootCtx, 2*time.Hour)
	go startUnknownLim.Cleanup(rootCtx, 2*time.Hour)
	go verifyIPLim.Cleanup(rootCtx, 30*time.Minute)
	go unsubIPLim.Cleanup(rootCtx, 30*time.Minute)
	go grantLim.Cleanup(rootCtx, 30*time.Minute)

	// Turnstile: requerido en prod, opcional en dev. Mismo patrón que
	// UNSUBSCRIBE_SECRET — SECURE_COOKIES es el discriminante.
	tsSiteKey := os.Getenv("TURNSTILE_SITE_KEY")
	tsSecret := os.Getenv("TURNSTILE_SECRET_KEY")
	var tsVerifier *turnstile.Verifier
	if tsSiteKey == "" || tsSecret == "" {
		if envBool("SECURE_COOKIES", false) {
			log.Fatal("TURNSTILE_SITE_KEY y TURNSTILE_SECRET_KEY requeridos en producción")
		}
		logger.Warn("Turnstile no configurado: /auth/start sin reto (solo dev)")
		// Si solo una de las dos vino seteada, tsVerifier queda nil (Verify
		// retorna true siempre) pero sin esto tsSiteKey seguiría no-vacía: el
		// widget se renderizaría y el usuario tendría que resolverlo, para un
		// reto que en realidad no valida nada. Peor que no tener reto.
		tsSiteKey = ""
	} else {
		tsVerifier = turnstile.New(tsSecret)
	}

	authHandler, err := auth.New(auth.Config{
		DB:                  db,
		Sessions:            sessions,
		Email:               emailClient,
		HTMLTemplates:       htmlFS,
		EmailTemplates:      emailFS,
		BaseURL:             envOr("BASE_URL", "http://localhost:8080"),
		Logger:              logger,
		TrustProxy:          envBool("TRUST_PROXY", false),
		StartIPLimiter:      startIPLim,
		StartEmailLimiter:   startEmailLim,
		StartKnownLimiter:   startKnownLim,
		StartUnknownLimiter: startUnknownLim,
		VerifyIPLimiter:     verifyIPLim,
		Errs:                errs,
		Turnstile:           tsVerifier,
		TurnstileSiteKey:    tsSiteKey,
	})
	if err != nil {
		log.Fatalf("configurar auth: %v", err)
	}

	// Multi-source release tracking (ADR 0014). Cada source es un
	// releasesource.Client; main wirea el map por nombre. Cada source es
	// opt-in vía su propia env var; default on para preservar el
	// comportamiento histórico de mokuji.org. Si las 3 quedan apagadas,
	// el binario arranca igual con warning — la biblioteca queda como
	// catálogo manual sin tracking automático.
	sourceClients := map[string]releasesource.Client{}
	sourceMatchers := map[string]library.Matcher{}
	if envBool("MANGADEX_ENABLED", true) {
		mangadexClient := mangadex.New()
		sourceClients[mangadexClient.Name()] = mangadexClient
		sourceMatchers[mangadexClient.Name()] = matcher.New(mangadexClient)
		logger.Info("source mangadex habilitada")
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

	// Worker periódico que polleea cada source (ADR 0014 §5). Solo se
	// construye si hay al menos una source habilitada; con todas off, el
	// binario funciona como catálogo manual y skipea el polling.
	var releasesWorker *worker.Worker
	if len(sourceClients) == 0 {
		logger.Warn("ninguna source de releases habilitada — el worker no arranca. La biblioteca queda como catálogo manual sin tracking automático.")
	} else {
		releasesWorker, err = worker.New(worker.Config{
			DB:      db,
			Clients: sourceClients,
			Logger:  logger,
		})
		if err != nil {
			log.Fatalf("configurar release worker: %v", err)
		}
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

	// Flasher: cookie one-shot para feedback post-redirect. Stateless.
	// Comparte el flag SECURE_COOKIES con sessions — en prod (HTTPS)
	// las cookies viajan con Secure, en dev local sin TLS no.
	flasher := flash.New(envBool("SECURE_COOKIES", false))

	// Un solo cliente de AniList: catálogo de búsqueda y resolver del import
	// de MAL (idMal).
	anilistClient := anilist.New()

	libraryHandler, err := library.New(library.Config{
		DB:             db,
		MangaSource:    anilistClient,
		MALResolver:    anilistClient,
		Flasher:        flasher,
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
		Errs:          errs,
	})
	if err != nil {
		log.Fatalf("configurar library: %v", err)
	}

	profileHandler, err := profile.New(profile.Config{
		DB:             db,
		HTMLTemplates:  htmlFS,
		EmailTemplates: emailFS,
		Email:          emailClient,
		BaseURL:        envOr("BASE_URL", "http://localhost:8080"),
		Flasher:        flasher,
		Logger:         logger,
		Errs:           errs,
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
		Errs:          errs,
	})
	if err != nil {
		log.Fatalf("configurar releases: %v", err)
	}

	onboardingHandler, err := onboarding.New(onboarding.Config{
		DB:            db,
		HTMLTemplates: htmlFS,
		Logger:        logger.With("component", "onboarding"),
	})
	if err != nil {
		log.Fatalf("crear handler onboarding: %v", err)
	}

	// home se construye después de releasesHandler porque el widget de
	// "últimos releases" en / reusa el método UnreadReleases. El orden
	// importa solo para wiring; no hay ciclo de import (home → releases).
	homeHandler, err := home.New(home.Config{
		DB:            db,
		HTMLTemplates: htmlFS,
		Releases:      releasesHandler,
		Logger:        logger,
		Errs:          errs,
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
	var feedbackHandler *feedback.Handler
	if feedbackTo := os.Getenv("FEEDBACK_EMAIL_TO"); feedbackTo != "" {
		feedbackHandler, err = feedback.New(feedback.Config{
			Templates: htmlFS,
			Email:     emailClient,
			EmailTo:   feedbackTo,
			Limiter:   ratelimit.New(rate.Every(12*time.Minute), 5), // 5/hora
			Flasher:   flasher,
			Logger:    logger,
			UserEmail: func(ctx context.Context, userID int64) (string, error) {
				var email string
				err := db.QueryRowContext(ctx,
					`SELECT email FROM users WHERE id = ?`, userID,
				).Scan(&email)
				if err != nil {
					return "", err
				}
				return email, nil
			},
		})
		if err != nil {
			logger.Error("inicializar feedback handler", "err", err)
			os.Exit(1)
		}
		feedback.SetEnabled(true)
		logger.Info("feedback habilitado", "to", feedbackTo)
	}

	adminHandler, err := admin.New(admin.Config{
		DB:           db,
		Token:        os.Getenv("ADMIN_TOKEN"),
		Digest:       digestSender,
		Health:       releasesWorker,
		Logger:       logger,
		BaseURL:      envOr("BASE_URL", "http://localhost:8080"),
		GrantLimiter: grantLim,
		TrustProxy:   envBool("TRUST_PROXY", false),
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
	var backfiller *library.Backfiller
	if len(sourceMatchers) > 0 {
		backfiller, err = library.NewBackfiller(library.BackfillConfig{
			DB:             db,
			SourceMatchers: sourceMatchers,
			Logger:         logger,
		})
		if err != nil {
			log.Fatalf("configurar backfiller: %v", err)
		}
		go backfiller.Run(rootCtx)
	}

	go cleanupSessions(rootCtx, sessions, logger)
	go cleanupAuthArtifacts(rootCtx, db, logger)
	if releasesWorker != nil {
		go releasesWorker.Run(rootCtx)
	}

	srv := server.New(server.Config{
		Addr:          envOr("HTTP_ADDR", ":8080"),
		BaseURL:       envOr("BASE_URL", "http://localhost:8080"),
		DB:            db,
		Logger:        logger,
		Sessions:      sessions,
		Auth:          authHandler,
		Home:          homeHandler,
		Library:       libraryHandler,
		Onboarding:    onboardingHandler,
		Profile:       profileHandler,
		Releases:      releasesHandler,
		Unsubscribe:   unsubHandler,
		Admin:         adminHandler,
		Feedback:      feedbackHandler,
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

// cleanupAuthArtifacts drena periódicamente los códigos OTP vencidos y los
// grants de break-glass vencidos. Un solo tick para ambos: son dos DELETEs
// baratos sobre tablas chicas y no hay razón para dos goroutines.
func cleanupAuthArtifacts(ctx context.Context, db *sql.DB, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := auth.CleanupExpiredCodes(ctx, db); err != nil {
				logger.Error("cleanup otp_codes", "err", err)
			} else if n > 0 {
				logger.Info("otp_codes vencidos borrados", "filas", n)
			}
			if err := grant.CleanupExpired(ctx, db); err != nil {
				logger.Error("cleanup admin_grants", "err", err)
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
