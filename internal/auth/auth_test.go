package auth

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"
	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/code"
	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/grant"
	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/ratelimit"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/turnstile"
)

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := migrations.Run(db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("aplicar migrations: %v", err)
	}
	return db
}

func newTestHandler(t *testing.T, db *sql.DB) *Handler {
	t.Helper()
	return &Handler{
		db:     db,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// Los tests de generate/format/sanitize/valid/hash viven en
// internal/code/code_test.go — ese paquete es el dueño de los helpers.

func TestStoreAndConsumeCode(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	ctx := context.Background()

	if _, err := h.storeCode(ctx, "alice@example.com", "123456", "1.2.3.4", "UA"); err != nil {
		t.Fatalf("storeCode: %v", err)
	}

	ok, err := h.consumeCode(ctx, "alice@example.com", "123456")
	if err != nil {
		t.Fatalf("consumeCode: %v", err)
	}
	if !ok {
		t.Error("código válido no fue consumido")
	}

	// Segundo intento con el mismo código debe fallar (single-use).
	ok, err = h.consumeCode(ctx, "alice@example.com", "123456")
	if err != nil {
		t.Fatalf("consumeCode segunda vez: %v", err)
	}
	if ok {
		t.Error("código ya usado fue aceptado de nuevo")
	}
}

func TestConsumeWrongCode(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	ctx := context.Background()

	if _, err := h.storeCode(ctx, "alice@example.com", "123456", "", ""); err != nil {
		t.Fatal(err)
	}

	ok, err := h.consumeCode(ctx, "alice@example.com", "999999")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("código incorrecto fue aceptado")
	}
}

func TestConsumeWrongEmail(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	ctx := context.Background()

	if _, err := h.storeCode(ctx, "alice@example.com", "123456", "", ""); err != nil {
		t.Fatal(err)
	}

	ok, err := h.consumeCode(ctx, "bob@example.com", "123456")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("código de otro email fue aceptado")
	}
}

func TestConsumeExpiredCode(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	ctx := context.Background()

	// Insertar un código ya expirado directamente.
	past := time.Now().UTC().Add(-1 * time.Hour).Unix()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO otp_codes (email, code_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?)
	`, "alice@example.com", code.Hash("123456"), past, past); err != nil {
		t.Fatal(err)
	}

	ok, err := h.consumeCode(ctx, "alice@example.com", "123456")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("código expirado fue aceptado")
	}
}

func TestFindOrCreateUserCreates(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	ctx := context.Background()

	id, err := h.findOrCreateUser(ctx, "new@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Errorf("id inválido: %d", id)
	}

	var email string
	if err := db.QueryRow(`SELECT email FROM users WHERE id = ?`, id).Scan(&email); err != nil {
		t.Fatal(err)
	}
	if email != "new@example.com" {
		t.Errorf("email: esperado new@example.com, obtenido %q", email)
	}
}

func TestFindOrCreateUserReturnsExisting(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	ctx := context.Background()

	// Crear primero
	id1, err := h.findOrCreateUser(ctx, "existing@example.com", true)
	if err != nil {
		t.Fatal(err)
	}

	// Segunda invocación debe devolver el mismo id, sin crear nuevo.
	id2, err := h.findOrCreateUser(ctx, "existing@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("ids distintos: %d vs %d", id1, id2)
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM users WHERE email = ?`, "existing@example.com").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("se creó duplicado: %d filas", count)
	}
}

func TestFindOrCreateUserPersistsDigestOptInOnSignup(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	ctx := context.Background()

	cases := []struct {
		name   string
		optIn  bool
		expect int
	}{
		{"opt-in true", true, 1},
		{"opt-in false", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			email := "u-" + tc.name + "@example.com"
			id, err := h.findOrCreateUser(ctx, email, tc.optIn)
			if err != nil {
				t.Fatal(err)
			}
			var got int
			if err := db.QueryRow(`SELECT email_digest_enabled FROM users WHERE id = ?`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.expect {
				t.Errorf("email_digest_enabled: esperado %d, obtenido %d", tc.expect, got)
			}
		})
	}
}

func TestFindOrCreateUserPreservesDigestOnLogin(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	ctx := context.Background()

	// Signup con opt-in=false (digest desactivado).
	id, err := h.findOrCreateUser(ctx, "alice@example.com", false)
	if err != nil {
		t.Fatal(err)
	}

	// "Login" (re-invocación) con opt-in=true NO debe activarlo.
	if _, err := h.findOrCreateUser(ctx, "alice@example.com", true); err != nil {
		t.Fatal(err)
	}

	var got int
	if err := db.QueryRow(`SELECT email_digest_enabled FROM users WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("login no debe alterar digest existente; esperado 0, obtenido %d", got)
	}
}

// newAuthTestHandler arma un Handler real (templates de disco + sender SMTP
// fake que cuenta envíos) para ejercitar el POST /auth/start end-to-end.
// sent acumula los envíos. opts recibe la Config con los defaults ya
// puestos y puede pisar los campos que le interesen a cada test; es la base
// común de newStartHandler, newBucketHandler y newTurnstileHandler, que
// antes repetían este mismo armado con solo la Config final distinta.
func newAuthTestHandler(t *testing.T, db *sql.DB, sent *int, opts func(*Config)) *Handler {
	t.Helper()
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	ec, err := email.New(email.Config{
		Host: "localhost", Port: 1025, From: "mokuji <auth@mokuji.org>", Logger: discard,
	})
	if err != nil {
		t.Fatalf("email.New: %v", err)
	}
	ec.SetSendMailForTesting(func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		*sent++
		return nil
	})
	cfg := Config{
		DB:             db,
		Sessions:       session.New(db, false, discard),
		Email:          ec,
		HTMLTemplates:  os.DirFS("../../web/templates"),
		EmailTemplates: os.DirFS("../../web/emails"),
		Logger:         discard,
	}
	if opts != nil {
		opts(&cfg)
	}
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// newStartHandler cablea globalLim como StartUnknownLimiter: los tests que
// usan este helper mandan direcciones sin cuenta, así que el bucket de
// desconocidos es el que corresponde.
func newStartHandler(t *testing.T, db *sql.DB, globalLim *ratelimit.Limiter, sent *int) *Handler {
	t.Helper()
	return newAuthTestHandler(t, db, sent, func(cfg *Config) {
		cfg.StartUnknownLimiter = globalLim
	})
}

func postStart(h *Handler, email string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/auth/start", strings.NewReader("email="+email))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.start(w, req)
	return w
}

// newBucketHandler es como newStartHandler pero cablea known/unknown por
// separado, para los tests que necesitan controlarlos de forma independiente.
func newBucketHandler(t *testing.T, db *sql.DB, known, unknown *ratelimit.Limiter, sent *int) *Handler {
	t.Helper()
	return newAuthTestHandler(t, db, sent, func(cfg *Config) {
		cfg.StartKnownLimiter = known
		cfg.StartUnknownLimiter = unknown
	})
}

func TestStartUsuarioConocidoNoConsumeBucketDesconocidos(t *testing.T) {
	db := setupDB(t)
	if _, err := db.Exec(
		`INSERT INTO users (id, email, created_at) VALUES (1, 'alice@example.com', ?)`,
		time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("sembrar user: %v", err)
	}

	sent := 0
	known := ratelimit.New(rate.Every(time.Minute), 5)
	// Desconocidos agotado: si alice cayera en este bucket, no se enviaría.
	unknown := ratelimit.New(rate.Every(time.Hour), 0)
	h := newBucketHandler(t, db, known, unknown, &sent)

	w := postStart(h, "alice@example.com")

	if sent != 1 {
		t.Errorf("un user conocido debió enviarse pese al bucket de desconocidos agotado, sent=%d", sent)
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("esperaba 303, got %d", w.Code)
	}
}

func TestStartDesconocidoNoConsumeBucketConocidos(t *testing.T) {
	db := setupDB(t)
	sent := 0
	// Conocidos con presupuesto, desconocidos agotado.
	known := ratelimit.New(rate.Every(time.Minute), 5)
	unknown := ratelimit.New(rate.Every(time.Hour), 0)
	h := newBucketHandler(t, db, known, unknown, &sent)

	w := postStart(h, "random@example.com")

	if sent != 0 {
		t.Errorf("un email desconocido se envió con su bucket agotado, sent=%d", sent)
	}
	if w.Code == http.StatusSeeOther {
		t.Error("redirigió a verify; debía mostrar error sin enviar")
	}
}

// TestStartBucketConocidoAgotadoNoEnvia cubre el espejo de
// TestStartDesconocidoNoConsumeBucketConocidos: hasta ahora solo estaba
// probado el agotamiento del bucket de desconocidos. Es el bucket que los
// findings 2 y 3 de la revisión final atacan (una dirección conocida
// quemada podía drenarlo sin resolver Turnstile ni llegar al cold-list), así
// que el agotamiento del lado known necesita su propio test.
func TestStartBucketConocidoAgotadoNoEnvia(t *testing.T) {
	db := setupDB(t)
	if _, err := db.Exec(
		`INSERT INTO users (id, email, created_at) VALUES (1, 'alice@example.com', ?)`,
		time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("sembrar user: %v", err)
	}

	sent := 0
	// Conocidos agotado, desconocidos con presupuesto de sobra.
	known := ratelimit.New(rate.Every(time.Hour), 0)
	unknown := ratelimit.New(rate.Every(time.Minute), 5)
	h := newBucketHandler(t, db, known, unknown, &sent)

	w := postStart(h, "alice@example.com")

	if sent != 0 {
		t.Errorf("un user conocido se envió con su bucket agotado, sent=%d", sent)
	}
	if w.Code == http.StatusSeeOther {
		t.Error("redirigió a verify; debía mostrar error sin enviar")
	}
}

func TestStartFloodDeDesconocidosNoBloqueaAlConocido(t *testing.T) {
	db := setupDB(t)
	if _, err := db.Exec(
		`INSERT INTO users (id, email, created_at) VALUES (1, 'alice@example.com', ?)`,
		time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("sembrar user: %v", err)
	}

	sent := 0
	known := ratelimit.New(rate.Every(time.Minute), 5)
	unknown := ratelimit.New(rate.Every(time.Hour), 2)
	h := newBucketHandler(t, db, known, unknown, &sent)

	// Vaciar el bucket de desconocidos con direcciones distintas, como haría
	// el atacante. Los limiters por-email no lo frenan porque cada dirección
	// estrena bucket propio.
	for _, addr := range []string{"a@x.com", "b@x.com", "c@x.com", "d@x.com"} {
		postStart(h, addr)
	}
	sentTrasFlood := sent

	w := postStart(h, "alice@example.com")

	if sent != sentTrasFlood+1 {
		t.Errorf("el flood de desconocidos bloqueó al user conocido (sent %d -> %d)", sentTrasFlood, sent)
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("esperaba 303 para el conocido, got %d", w.Code)
	}
}

func TestRenderOTPEmail(t *testing.T) {
	// Test directo de renderOTPEmail con plantilla cargada desde disco.
	bs, err := os.ReadFile("../../web/emails/otp.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bs), "{{.Code}}") {
		t.Fatal("plantilla otp.txt no contiene {{.Code}}; cambió la API?")
	}
	// El render real se prueba indirectamente vía test del handler en commits
	// futuros cuando agreguemos integración end-to-end.
}

func TestGrantCanjeaTokenYCreaSesion(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 5), &sent)

	if _, err := db.Exec(
		`INSERT INTO users (id, email, created_at) VALUES (1, 'alice@example.com', ?)`,
		time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("sembrar user: %v", err)
	}
	token, err := grant.Issue(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/grant?t="+token, nil)
	w := httptest.NewRecorder()
	h.grant(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("esperaba 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("esperaba redirect a /, got %q", loc)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = 1`).Scan(&n); err != nil {
		t.Fatalf("contar sesiones: %v", err)
	}
	if n != 1 {
		t.Errorf("esperaba 1 sesión creada, got %d", n)
	}
}

func TestGrantRechazaTokenInvalido(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 5), &sent)

	req := httptest.NewRequest(http.MethodGet, "/auth/grant?t=basura", nil)
	w := httptest.NewRecorder()
	h.grant(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("esperaba 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth" {
		t.Errorf("esperaba redirect a /auth con token inválido, got %q", loc)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("contar sesiones: %v", err)
	}
	if n != 0 {
		t.Errorf("se creó sesión con token inválido (n=%d)", n)
	}
}

func TestGrantNoReusableTrasCanje(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 5), &sent)

	if _, err := db.Exec(
		`INSERT INTO users (id, email, created_at) VALUES (1, 'alice@example.com', ?)`,
		time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("sembrar user: %v", err)
	}
	token, err := grant.Issue(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	h.grant(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/auth/grant?t="+token, nil))

	w := httptest.NewRecorder()
	h.grant(w, httptest.NewRequest(http.MethodGet, "/auth/grant?t="+token, nil))
	if w.Code != http.StatusSeeOther {
		t.Errorf("esperaba 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth" {
		t.Errorf("el token se canjeó dos veces (redirect=%q)", loc)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("contar sesiones: %v", err)
	}
	if n != 1 {
		t.Errorf("esperaba exactamente 1 sesión tras dos canjes, got %d", n)
	}
}

func TestCleanupExpiredCodesRespetaRetencion(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Tres filas: vigente, vencida hace 1h (dentro de la retención de 24h),
	// y vencida hace 48h (fuera).
	rows := []struct {
		email   string
		expires int64
	}{
		{"vigente@example.com", now.Add(10 * time.Minute).Unix()},
		{"reciente@example.com", now.Add(-time.Hour).Unix()},
		{"vieja@example.com", now.Add(-48 * time.Hour).Unix()},
	}
	for _, row := range rows {
		if _, err := db.Exec(`
			INSERT INTO otp_codes (email, code_hash, created_at, expires_at)
			VALUES (?, 'hash', ?, ?)
		`, row.email, now.Unix(), row.expires); err != nil {
			t.Fatalf("sembrar %s: %v", row.email, err)
		}
	}

	deleted, err := CleanupExpiredCodes(ctx, db)
	if err != nil {
		t.Fatalf("CleanupExpiredCodes: %v", err)
	}
	if deleted != 1 {
		t.Errorf("esperaba 1 fila borrada (solo la de 48h), got %d", deleted)
	}

	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM otp_codes`).Scan(&remaining); err != nil {
		t.Fatalf("contar: %v", err)
	}
	if remaining != 2 {
		t.Errorf("esperaba 2 filas restantes, got %d", remaining)
	}

	// La fila reciente tiene que sobrevivir: la cold-list la necesita.
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM otp_codes WHERE email = 'reciente@example.com'`,
	).Scan(&n); err != nil {
		t.Fatalf("contar reciente: %v", err)
	}
	if n != 1 {
		t.Error("el reaper borró una fila dentro de la ventana de 24h que usa la cold-list")
	}
}

func TestStartColdListBloqueaDireccionQuemada(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 20), &sent)
	now := time.Now().UTC()

	// Dos códigos recientes sin verificar: la firma del bombing.
	for range 2 {
		if _, err := db.Exec(`
			INSERT INTO otp_codes (email, code_hash, created_at, expires_at)
			VALUES ('quemada@example.com', 'hash', ?, ?)
		`, now.Unix(), now.Add(-time.Minute).Unix()); err != nil {
			t.Fatalf("sembrar código: %v", err)
		}
	}

	w := postStart(h, "quemada@example.com")

	if sent != 0 {
		t.Errorf("se envió a una dirección en cold-list, sent=%d", sent)
	}
	if w.Code == http.StatusSeeOther {
		t.Error("redirigió a verify; debía frenar sin enviar")
	}
}

func TestStartColdListPermiteUnReintento(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 20), &sent)
	now := time.Now().UTC()

	// Un solo código sin verificar: el caso del user legítimo cuyo primer
	// correo cayó en spam. El umbral es 2 justamente para no castigarlo.
	if _, err := db.Exec(`
		INSERT INTO otp_codes (email, code_hash, created_at, expires_at)
		VALUES ('legitima@example.com', 'hash', ?, ?)
	`, now.Unix(), now.Add(-time.Minute).Unix()); err != nil {
		t.Fatalf("sembrar código: %v", err)
	}

	postStart(h, "legitima@example.com")

	if sent != 1 {
		t.Errorf("un solo código previo debió permitir reintento, sent=%d", sent)
	}
}

func TestStartColdListIgnoraCodigosViejos(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 20), &sent)
	now := time.Now().UTC()

	// Dos códigos sin verificar pero de hace 48h: fuera de la ventana. El
	// rastro de un ataque viejo no puede dejar una dirección bloqueada.
	for range 2 {
		if _, err := db.Exec(`
			INSERT INTO otp_codes (email, code_hash, created_at, expires_at)
			VALUES ('vieja@example.com', 'hash', ?, ?)
		`, now.Add(-48*time.Hour).Unix(), now.Add(-48*time.Hour).Unix()); err != nil {
			t.Fatalf("sembrar código: %v", err)
		}
	}

	postStart(h, "vieja@example.com")

	if sent != 1 {
		t.Errorf("códigos de hace 48h no debían bloquear, sent=%d", sent)
	}
}

func TestStartColdListIgnoraCodigosVerificados(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 20), &sent)
	now := time.Now().UTC()

	// Dos códigos recientes, ambos verificados: es un user activo, no una
	// víctima de bombing.
	for range 2 {
		if _, err := db.Exec(`
			INSERT INTO otp_codes (email, code_hash, created_at, expires_at, used_at)
			VALUES ('activa@example.com', 'hash', ?, ?, ?)
		`, now.Unix(), now.Add(-time.Minute).Unix(), now.Unix()); err != nil {
			t.Fatalf("sembrar código: %v", err)
		}
	}

	postStart(h, "activa@example.com")

	if sent != 1 {
		t.Errorf("códigos ya verificados no debían bloquear, sent=%d", sent)
	}
}

// TestStartFalloDeEnvioNoCuentaParaColdList cubre el finding 1 de la
// revisión final: si SendMessage falla (proveedor caído, quota agotada, key
// revocada), la fila que storeCode insertó no puede quedar contando como
// intento quemado. Si quedara, un outage cold-listea usuarios reales por 24h
// después de que el envío se restablezca — justo lo que iba a pasarle a
// producción con la key de Resend revocada.
func TestStartFalloDeEnvioNoCuentaParaColdList(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 20), &sent)

	// El proveedor está caído: todo intento de envío falla.
	h.email.SetSendMailForTesting(func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		return errors.New("smtp: conexión rechazada")
	})

	for i := range coldListThreshold + 1 {
		w := postStart(h, "victima@example.com")
		if w.Code != http.StatusInternalServerError {
			t.Errorf("intento %d: esperaba 500 por fallo de envío, got %d", i, w.Code)
		}
	}
	if sent != 0 {
		t.Errorf("sent=%d, el sender fake siempre falla", sent)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM otp_codes WHERE email = ?`, "victima@example.com").Scan(&rows); err != nil {
		t.Fatalf("contar otp_codes: %v", err)
	}
	if rows != 0 {
		t.Errorf("quedaron %d filas de otp_codes tras fallos de envío; storeCode debía revertirse", rows)
	}

	// El proveedor se recupera: el próximo intento debe mandar el correo,
	// sin que los fallos previos (más que coldListThreshold) hayan
	// cold-listeado la dirección.
	h.email.SetSendMailForTesting(func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		sent++
		return nil
	})
	w := postStart(h, "victima@example.com")
	if w.Code != http.StatusSeeOther {
		t.Errorf("tras restaurar el envío esperaba 303, got %d", w.Code)
	}
	if sent != 1 {
		t.Errorf("esperaba 1 envío tras restaurar el proveedor, sent=%d", sent)
	}
}

// TestStartRevierteConContextoCancelado cubre el defecto que sobrevivió a la
// revisión anterior: deleteCode recibía r.Context(), y SendMail no tiene
// deadline. Un proveedor que blackholea paquetes cuelga el handler el
// timeout de conexión TCP del SO (un minuto o más); el usuario abandona el
// login antes de eso, el cliente se desconecta, y Go cancela r.Context(). Con
// ese contexto, database/sql se niega a entregar una conexión para el DELETE
// — falla garantizado, no solo probable — y la fila sobrevive con
// used_at IS NULL. Dos así cold-listean a un usuario real por 24h.
//
// El contexto se cancela desde DENTRO del sender fake, justo antes de que
// devuelva el error: el trabajo de DB previo (unverifiedCount, storeCode)
// corre con el contexto todavía vivo, y solo el envío lo encuentra
// cancelado. Cancelar de entrada no sirve — el handler haría el DB work
// antes del send y fallaría ahí, sin llegar nunca a probar el rollback.
func TestStartRevierteConContextoCancelado(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 20), &sent)

	ctx, cancel := context.WithCancel(context.Background())
	h.email.SetSendMailForTesting(func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		// El cliente se desconectó mientras el envío colgaba: esto es lo
		// que Go hace a r.Context() en ese momento.
		cancel()
		return errors.New("smtp: conexión rechazada")
	})

	req := httptest.NewRequest(http.MethodPost, "/auth/start", strings.NewReader("email=abandonado@example.com"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.start(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("esperaba 500 por fallo de envío, got %d", w.Code)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM otp_codes WHERE email = ?`, "abandonado@example.com").Scan(&rows); err != nil {
		t.Fatalf("contar otp_codes: %v", err)
	}
	if rows != 0 {
		t.Errorf("quedó %d fila(s) de otp_codes con el contexto del request ya cancelado; deleteCode debía sobrevivir a la cancelación", rows)
	}
}

// TestStartColdListNoConsumeBucketConocido cubre el finding 2: el cold-list
// tiene que rechazar antes de que el bucket de envío gaste un token. Si el
// orden fuera al revés, una dirección quemada drenaría el bucket compartido
// (known/unknown) sin mandar nada, dejando sin presupuesto a cualquier otra
// dirección del mismo tipo — el DoS de acceso de v1.0.4 reconstruido adentro
// del bucket partido.
func TestStartColdListNoConsumeBucketConocido(t *testing.T) {
	db := setupDB(t)
	for _, addr := range []string{"quemada@example.com", "sana@example.com"} {
		if _, err := db.Exec(
			`INSERT INTO users (email, created_at) VALUES (?, ?)`, addr, time.Now().UTC().Unix(),
		); err != nil {
			t.Fatalf("sembrar user %s: %v", addr, err)
		}
	}
	now := time.Now().UTC()
	for range 2 {
		if _, err := db.Exec(`
			INSERT INTO otp_codes (email, code_hash, created_at, expires_at)
			VALUES ('quemada@example.com', 'hash', ?, ?)
		`, now.Unix(), now.Add(-time.Minute).Unix()); err != nil {
			t.Fatalf("sembrar código: %v", err)
		}
	}

	sent := 0
	// Bucket known con un solo token disponible: si quemada@ lo gastara al
	// pasar por el bucket antes de que el cold-list la frene, no quedaría
	// nada para sana@.
	known := ratelimit.New(rate.Every(time.Hour), 1)
	unknown := ratelimit.New(rate.Every(time.Hour), 0)
	h := newBucketHandler(t, db, known, unknown, &sent)

	w1 := postStart(h, "quemada@example.com")
	if w1.Code == http.StatusSeeOther {
		t.Error("la dirección en cold-list se envió")
	}

	w2 := postStart(h, "sana@example.com")
	if w2.Code != http.StatusSeeOther {
		t.Errorf("el cold-list de quemada@ drenó el bucket de conocidos; sana@ no recibió su código, got %d", w2.Code)
	}
	if sent != 1 {
		t.Errorf("esperaba exactamente 1 envío (a sana@), sent=%d", sent)
	}
}

func newTurnstileHandler(t *testing.T, db *sql.DB, v *turnstile.Verifier, sent *int) *Handler {
	t.Helper()
	return newAuthTestHandler(t, db, sent, func(cfg *Config) {
		cfg.StartUnknownLimiter = ratelimit.New(rate.Every(time.Minute), 20)
		cfg.Turnstile = v
		cfg.TurnstileSiteKey = "site-key-de-prueba"
	})
}

func turnstileFake(t *testing.T, success bool) *turnstile.Verifier {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if success {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	t.Cleanup(srv.Close)
	v := turnstile.New("secreto")
	v.SetEndpointForTesting(srv.URL)
	return v
}

// turnstileFakeInalcanzable arma un Verifier que apunta a un endpoint donde
// no hay nada escuchando, para forzar el camino de error de red de Verify
// (Cloudflare caído/inalcanzable) en vez del camino de {"success":false}.
// Arranca un httptest.Server real y lo cierra antes de devolver el
// Verifier: la URL queda apuntando a un puerto ya liberado, así que
// cualquier request choca con connection refused.
func turnstileFakeInalcanzable(t *testing.T) *turnstile.Verifier {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := srv.URL
	srv.Close()
	v := turnstile.New("secreto")
	v.SetEndpointForTesting(endpoint)
	return v
}

func postStartConTurnstile(h *Handler, emailAddr, tsToken string) *httptest.ResponseRecorder {
	form := url.Values{}
	form.Set("email", emailAddr)
	form.Set("cf-turnstile-response", tsToken)
	req := httptest.NewRequest(http.MethodPost, "/auth/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.start(w, req)
	return w
}

func TestStartRechazaSinTurnstile(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newTurnstileHandler(t, db, turnstileFake(t, true), &sent)

	w := postStartConTurnstile(h, "alice@example.com", "")

	if sent != 0 {
		t.Errorf("se envió sin resolver el reto, sent=%d", sent)
	}
	if w.Code != http.StatusOK {
		t.Errorf("esperaba 200 con el form de vuelta, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "data-sitekey") {
		t.Error("el render no incluye data-sitekey; el widget no aparece")
	}
}

func TestStartRechazaTurnstileInvalido(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newTurnstileHandler(t, db, turnstileFake(t, false), &sent)

	w := postStartConTurnstile(h, "alice@example.com", "token-falso")

	if sent != 0 {
		t.Errorf("se envió con reto inválido, sent=%d", sent)
	}
	if w.Code != http.StatusOK {
		t.Errorf("esperaba 200 con el form de vuelta, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "data-sitekey") {
		t.Error("el render no incluye data-sitekey; el widget no aparece")
	}
}

// TestStartFailCierraSiCloudflareInalcanzable cubre la rama de error de
// Verify (Cloudflare caído/inalcanzable), no solo el veredicto false.
// turnstileFake nunca produce un error de transporte, así que sin este test
// esa rama de start —el `if err != nil { ... }` que loguea y rechaza— corre
// sin ningún test encima. Si alguien la reescribiera para tratar el error
// como fail-open (dejar pasar en vez de rechazar cuando Cloudflare no
// responde, para "no romper el login"), toda la suite existente seguiría en
// verde y reabriría el agujero del incidente de bombing. Este test sí lo
// agarra: verificado apagando el fail-closed a mano y viendo el test fallar
// antes de dejarlo como estaba (detalle en el reporte).
func TestStartFailCierraSiCloudflareInalcanzable(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newTurnstileHandler(t, db, turnstileFakeInalcanzable(t), &sent)

	w := postStartConTurnstile(h, "alice@example.com", "token-cualquiera")

	if sent != 0 {
		t.Errorf("se envió con Cloudflare inalcanzable, sent=%d", sent)
	}
	if w.Code == http.StatusSeeOther {
		t.Error("redirigió a verify con Cloudflare inalcanzable; el fail-closed no cerró")
	}
}

func TestStartEnviaConTurnstileValido(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newTurnstileHandler(t, db, turnstileFake(t, true), &sent)

	w := postStartConTurnstile(h, "alice@example.com", "token-bueno")

	if sent != 1 {
		t.Errorf("esperaba 1 envío con reto resuelto, sent=%d", sent)
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("esperaba 303, got %d", w.Code)
	}
}

func TestStartSinVerifierNoExigeReto(t *testing.T) {
	db := setupDB(t)
	sent := 0
	// Verifier nil = dev sin keys. El flow tiene que seguir andando.
	h := newTurnstileHandler(t, db, nil, &sent)

	w := postStartConTurnstile(h, "alice@example.com", "")

	if sent != 1 {
		t.Errorf("sin Verifier el envío debía proceder, sent=%d", sent)
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("esperaba 303, got %d", w.Code)
	}
}

// TestStartSinTurnstileNoConsumeLimiterPorEmail cubre el finding 3: el
// limiter por-email tiene que correr después de Turnstile. Si corriera
// antes, tres POSTs con el email de una víctima y sin resolver el reto
// vaciarían su bucket (burst 1 acá) sin costo de red para quien ataca —
// turnstile.Verify corta en seco un token vacío sin llamar a Cloudflare.
func TestStartSinTurnstileNoConsumeLimiterPorEmail(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newAuthTestHandler(t, db, &sent, func(cfg *Config) {
		cfg.StartUnknownLimiter = ratelimit.New(rate.Every(time.Minute), 20)
		cfg.StartEmailLimiter = ratelimit.New(rate.Every(time.Hour), 1)
		cfg.Turnstile = turnstileFake(t, true)
		cfg.TurnstileSiteKey = "site-key-de-prueba"
	})

	// Flood sin token de Turnstile: no debe tocar el bucket por-email de la
	// víctima, sin importar cuántas veces se repita.
	for range 3 {
		postStartConTurnstile(h, "victima@example.com", "")
	}

	w := postStartConTurnstile(h, "victima@example.com", "token-bueno")
	if w.Code != http.StatusSeeOther {
		t.Errorf("el flood sin turnstile agotó el limiter por-email de la víctima, got %d", w.Code)
	}
	if sent != 1 {
		t.Errorf("esperaba 1 envío tras resolver el reto, sent=%d", sent)
	}
}
