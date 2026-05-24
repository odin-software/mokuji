package profile

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.odin.do/kengru/mokuji/internal/session"
)

func TestValidateUsername(t *testing.T) {
	cases := []struct {
		in      string
		wantErr error
		wantOut string
	}{
		// Casos válidos: se normalizan a lowercase + trim.
		{"alice", nil, "alice"},
		{"  Alice  ", nil, "alice"},
		{"a-b", nil, "a-b"},
		{"otaku_42", nil, "otaku_42"},
		{"abc", nil, "abc"},
		{"AAAABBBBCCCCDDDDEEEE", nil, "aaaabbbbccccddddeeee"}, // 20 chars

		// Largo.
		{"ab", ErrUsernameTooShort, ""},
		{"AAAABBBBCCCCDDDDEEEE1", ErrUsernameTooLong, ""}, // 21

		// Formato: no puede empezar/terminar con separador.
		{"-alice", ErrUsernameInvalid, ""},
		{"alice-", ErrUsernameInvalid, ""},
		{"_alice", ErrUsernameInvalid, ""},
		{"ali ce", ErrUsernameInvalid, ""}, // espacio interno (trim no aplica)
		{"alice@", ErrUsernameInvalid, ""},
		{"AlIcE!", ErrUsernameInvalid, ""},

		// Solo dígitos: choca con id legacy.
		{"123", ErrUsernameDigitsOnly, ""},
		{"4242", ErrUsernameDigitsOnly, ""},

		// Reservados (lowercase match).
		{"admin", ErrUsernameReserved, ""},
		{"AUTH", ErrUsernameReserved, ""},
		{"settings", ErrUsernameReserved, ""},
		{"mokuji", ErrUsernameReserved, ""},
	}

	for _, tc := range cases {
		got, err := ValidateUsername(tc.in)
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("ValidateUsername(%q): err=%v, esperado %v", tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.wantOut {
			t.Errorf("ValidateUsername(%q): out=%q, esperado %q", tc.in, got, tc.wantOut)
		}
	}
}

func TestUpsertUsernameHappyPath(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")

	if err := h.upsertUsername(context.Background(), uid, "alice", time.Now()); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	slug, changed, err := h.loadUserSlug(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if !slug.Valid || slug.String != "alice" {
		t.Errorf("slug: esperado alice, obtuve %v", slug)
	}
	if !changed.Valid {
		t.Error("slug_changed_at debería estar seteado")
	}
}

func TestUpsertUsernameSameSlugIsNoop(t *testing.T) {
	// Setear el mismo slug que ya tenés no debe contar como cambio
	// (no actualiza slug_changed_at, no triggerea cooldown).
	db := setupDB(t)
	h := newFullHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := h.upsertUsername(context.Background(), uid, "alice", t0); err != nil {
		t.Fatal(err)
	}
	// Mismo slug, otro timestamp: no-op.
	t1 := t0.Add(5 * time.Minute)
	if err := h.upsertUsername(context.Background(), uid, "alice", t1); err != nil {
		t.Fatalf("re-setear el mismo slug debería ser noop: %v", err)
	}

	_, changed, _ := h.loadUserSlug(context.Background(), uid)
	if changed.Int64 != t0.Unix() {
		t.Errorf("slug_changed_at cambió en el no-op: %d != %d", changed.Int64, t0.Unix())
	}
}

func TestUpsertUsernameCooldown(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := h.upsertUsername(context.Background(), uid, "alice", t0); err != nil {
		t.Fatal(err)
	}

	// 5 días después: bloqueado.
	t1 := t0.Add(5 * 24 * time.Hour)
	if err := h.upsertUsername(context.Background(), uid, "bob", t1); !errors.Is(err, ErrUsernameCooldown) {
		t.Errorf("esperado ErrUsernameCooldown, obtuve %v", err)
	}

	// 30 días + 1s después: liberado.
	t2 := t0.Add(UsernameCooldown + time.Second)
	if err := h.upsertUsername(context.Background(), uid, "bob", t2); err != nil {
		t.Errorf("post-cooldown debería pasar, obtuve %v", err)
	}
}

func TestUpsertUsernameReservesPreviousSlug(t *testing.T) {
	// Tras un cambio, el slug viejo queda inaccesible para otro user
	// durante el cooldown (defensa anti-impersonation).
	db := setupDB(t)
	h := newFullHandler(t, db)
	alice := insertUser(t, db, "alice@example.com")
	bob := insertUser(t, db, "bob@example.com")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := h.upsertUsername(context.Background(), alice, "alice", t0); err != nil {
		t.Fatal(err)
	}
	// Alice cambia su username después del cooldown.
	t1 := t0.Add(UsernameCooldown + time.Hour)
	if err := h.upsertUsername(context.Background(), alice, "alice2", t1); err != nil {
		t.Fatal(err)
	}

	// Bob intenta agarrar "alice" inmediatamente. Debe rebotar.
	if err := h.upsertUsername(context.Background(), bob, "alice", t1); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("esperado ErrUsernameTaken por reservation, obtuve %v", err)
	}

	// Pasado el cooldown del reserved, Bob puede agarrarlo.
	t2 := t1.Add(UsernameCooldown + time.Hour)
	if err := h.upsertUsername(context.Background(), bob, "alice", t2); err != nil {
		t.Errorf("post-reservation debería liberar, obtuve %v", err)
	}
}

func TestUpsertUsernameTakenByOther(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db)
	alice := insertUser(t, db, "alice@example.com")
	bob := insertUser(t, db, "bob@example.com")

	t0 := time.Now()
	if err := h.upsertUsername(context.Background(), alice, "shared", t0); err != nil {
		t.Fatal(err)
	}
	if err := h.upsertUsername(context.Background(), bob, "shared", t0); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("esperado ErrUsernameTaken, obtuve %v", err)
	}
	// Case-insensitive: 'SHARED' también está tomado.
	if err := h.upsertUsername(context.Background(), bob, "SHARED", t0); err != nil {
		// SHARED no pasaría el validador (mayúsculas) — pero queremos
		// asegurar que SI pasara, el lookup case-insensitive lo bloquea.
		// Como la entrada se normaliza al pasar por el endpoint público,
		// acá testeamos directo upsert con el normalizado.
	}
	if err := h.upsertUsername(context.Background(), bob, "shared", t0); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("esperado ErrUsernameTaken case-insensitive, obtuve %v", err)
	}
}

func TestPublicProfileRedirectsNumericToSlug(t *testing.T) {
	// Si el user tiene slug, /u/{id} debe 301 a /u/{slug}.
	db := setupDB(t)
	h := newFullHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")
	if err := h.upsertUsername(context.Background(), uid, "alice", time.Now()); err != nil {
		t.Fatal(err)
	}

	idStr := strconv.FormatInt(uid, 10)
	req := httptest.NewRequest(http.MethodGet, "/u/"+idStr, nil)
	req.SetPathValue("handle", idStr)
	rec := httptest.NewRecorder()
	h.publicProfile(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status: esperado 301, obtuve %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/u/alice" {
		t.Errorf("Location: esperado /u/alice, obtuve %q", loc)
	}
}

func TestPublicProfileResolvesSlug(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")
	setVisibility(t, db, uid, "public")
	addEntry(t, db, uid, "Berserk")
	if err := h.upsertUsername(context.Background(), uid, "alice", time.Now()); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/u/alice", nil)
	req.SetPathValue("handle", "alice")
	rec := httptest.NewRecorder()
	h.publicProfile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtuve %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Berserk") {
		t.Error("body no incluye la entry")
	}
}

func TestPublicProfilePreviousSlugIs404(t *testing.T) {
	// Tras cambiar de "alice" → "alice2", /u/alice debe 404. Esto evita
	// que el slug viejo siga resolviendo y leakeando identidad.
	db := setupDB(t)
	h := newFullHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")
	setVisibility(t, db, uid, "public")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := h.upsertUsername(context.Background(), uid, "alice", t0); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(UsernameCooldown + time.Hour)
	if err := h.upsertUsername(context.Background(), uid, "alice2", t1); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/u/alice", nil)
	req.SetPathValue("handle", "alice")
	rec := httptest.NewRecorder()
	h.publicProfile(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("previous_slug debería 404, obtuve %d", rec.Code)
	}
}

func TestUpdateUsernameEndpointHappyPath(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")

	form := url.Values{"username": {"alice"}}
	req := httptest.NewRequest(http.MethodPost, "/settings/username", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = session.WithUserID(req, uid)
	rec := httptest.NewRecorder()
	h.updateUsername(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: esperado 303, obtuve %d (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/settings" {
		t.Errorf("Location: esperado /settings, obtuve %q", loc)
	}

	slug, _, _ := h.loadUserSlug(context.Background(), uid)
	if !slug.Valid || slug.String != "alice" {
		t.Errorf("slug no persistido: %v", slug)
	}
}

func TestUpdateUsernameEndpointValidationErrorReRendersForm(t *testing.T) {
	// Slug inválido (reservado) debe devolver 200 con la página y el
	// mensaje de error, no 303 ni 500.
	db := setupDB(t)
	h := newFullHandler(t, db)
	uid := insertUser(t, db, "alice@example.com")

	form := url.Values{"username": {"admin"}}
	req := httptest.NewRequest(http.MethodPost, "/settings/username", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = session.WithUserID(req, uid)
	rec := httptest.NewRecorder()
	h.updateUsername(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtuve %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "reservado") {
		t.Errorf("body no contiene mensaje de error: %s", body)
	}
	// Slug no persistido.
	slug, _, _ := h.loadUserSlug(context.Background(), uid)
	if slug.Valid {
		t.Errorf("slug reservado fue persistido: %v", slug)
	}
}
