package auth

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/code"
	"git.odin.do/kengru/mokuji/internal/migrations"
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
