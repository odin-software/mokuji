package auth

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/smtp"
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

	if err := h.storeCode(ctx, "alice@example.com", "123456", "1.2.3.4", "UA"); err != nil {
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

	if err := h.storeCode(ctx, "alice@example.com", "123456", "", ""); err != nil {
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

	if err := h.storeCode(ctx, "alice@example.com", "123456", "", ""); err != nil {
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

// newStartHandler arma un Handler real (templates de disco + sender SMTP
// fake que cuenta envíos) para ejercitar el POST /auth/start end-to-end.
// globalLim se cablea como StartUnknownLimiter: los tests que usan este
// helper mandan direcciones sin cuenta, así que el bucket de desconocidos es
// el que corresponde. sent acumula los envíos.
func newStartHandler(t *testing.T, db *sql.DB, globalLim *ratelimit.Limiter, sent *int) *Handler {
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
	h, err := New(Config{
		DB:                  db,
		Sessions:            session.New(db, false, discard),
		Email:               ec,
		HTMLTemplates:       os.DirFS("../../web/templates"),
		EmailTemplates:      os.DirFS("../../web/emails"),
		Logger:              discard,
		StartUnknownLimiter: globalLim,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
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
	h, err := New(Config{
		DB:                  db,
		Sessions:            session.New(db, false, discard),
		Email:               ec,
		HTMLTemplates:       os.DirFS("../../web/templates"),
		EmailTemplates:      os.DirFS("../../web/emails"),
		Logger:              discard,
		StartKnownLimiter:   known,
		StartUnknownLimiter: unknown,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
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

func newTurnstileHandler(t *testing.T, db *sql.DB, v *turnstile.Verifier, sent *int) *Handler {
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
	h, err := New(Config{
		DB:                  db,
		Sessions:            session.New(db, false, discard),
		Email:               ec,
		HTMLTemplates:       os.DirFS("../../web/templates"),
		EmailTemplates:      os.DirFS("../../web/emails"),
		Logger:              discard,
		StartUnknownLimiter: ratelimit.New(rate.Every(time.Minute), 20),
		Turnstile:           v,
		TurnstileSiteKey:    "site-key-de-prueba",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
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

func postStartConTurnstile(h *Handler, emailAddr, tsToken string) *httptest.ResponseRecorder {
	form := "email=" + emailAddr + "&cf-turnstile-response=" + tsToken
	req := httptest.NewRequest(http.MethodPost, "/auth/start", strings.NewReader(form))
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
	if w.Code == http.StatusSeeOther {
		t.Error("redirigió a verify sin reto resuelto")
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
	if w.Code == http.StatusSeeOther {
		t.Error("redirigió a verify con reto inválido")
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
