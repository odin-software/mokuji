package session

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("abrir DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schema, err := os.ReadFile("../../migrations/0001_users_and_sessions.sql")
	if err != nil {
		t.Fatalf("leer schema: %v", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("aplicar schema: %v", err)
	}
	return db
}

func insertUser(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO users (email, created_at) VALUES (?, ?)`,
		email, time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insertar usuario: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

func newManager(db *sql.DB) *Manager {
	return New(db, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func extractCookie(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestCreateAndLoad(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "alice@example.com")
	m := newManager(db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := m.Create(w, req, userID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := extractCookie(w, cookieName)
	if cookie == nil {
		t.Fatal("cookie no fue seteada")
	}
	if !cookie.HttpOnly {
		t.Error("cookie debería ser HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite: esperado Lax, obtenido %v", cookie.SameSite)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(cookie)
	s, err := m.Load(req2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s == nil {
		t.Fatal("sesión no encontrada")
	}
	if s.UserID != userID {
		t.Errorf("UserID: esperado %d, obtenido %d", userID, s.UserID)
	}
	if s.CSRFToken == "" {
		t.Error("CSRFToken vacío")
	}
}

func TestLoadMissingCookie(t *testing.T) {
	db := setupDB(t)
	m := newManager(db)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	s, err := m.Load(req)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s != nil {
		t.Error("esperado nil sin cookie")
	}
}

func TestLoadInvalidCookie(t *testing.T) {
	db := setupDB(t)
	m := newManager(db)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "garbage-no-existe"})
	s, err := m.Load(req)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s != nil {
		t.Error("esperado nil para cookie inválida")
	}
}

func TestLoadExpiredSession(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "expired@example.com")
	m := newManager(db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := m.Create(w, req, userID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := extractCookie(w, cookieName)

	past := time.Now().Add(-1 * time.Hour).Unix()
	if _, err := db.Exec(
		`UPDATE sessions SET expires_at = ? WHERE id_hash = ?`,
		past, hashID(cookie.Value),
	); err != nil {
		t.Fatalf("forzar expiración: %v", err)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(cookie)
	s, err := m.Load(req2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s != nil {
		t.Error("esperado nil para sesión expirada")
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("sesión expirada no fue limpiada: %d filas restantes", count)
	}
}

func TestDestroy(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "bob@example.com")
	m := newManager(db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := m.Create(w, req, userID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := extractCookie(w, cookieName)

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(cookie)
	if err := m.Destroy(w2, req2); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("esperado 0 sesiones, obtenido %d", count)
	}

	clearCookie := extractCookie(w2, cookieName)
	if clearCookie == nil {
		t.Fatal("cookie de limpieza no fue seteada")
	}
	if clearCookie.MaxAge >= 0 {
		t.Errorf("esperado MaxAge < 0, obtenido %d", clearCookie.MaxAge)
	}
}

func TestMiddleware(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "carol@example.com")
	m := newManager(db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := m.Create(w, req, userID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := extractCookie(w, cookieName)

	var captured *Session
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, _ := FromContext(r.Context())
		captured = s
	}))

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(cookie)
	handler.ServeHTTP(httptest.NewRecorder(), req2)

	if captured == nil {
		t.Fatal("sesión no fue adjuntada al context")
	}
	if captured.UserID != userID {
		t.Errorf("UserID: esperado %d, obtenido %d", userID, captured.UserID)
	}
}

func TestMiddlewareNoSessionForAnonymous(t *testing.T) {
	db := setupDB(t)
	m := newManager(db)

	called := false
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if _, ok := FromContext(r.Context()); ok {
			t.Error("no debería haber sesión para request anónimo")
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Error("handler no fue llamado")
	}
}

func TestCleanupExpired(t *testing.T) {
	db := setupDB(t)
	userID := insertUser(t, db, "cleanup@example.com")
	m := newManager(db)

	// Sesión expirada y sesión vigente
	now := time.Now().UTC()
	if _, err := db.Exec(`
		INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, last_used_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?)
	`,
		"hash-expired", userID, "csrf1", now.Unix(), now.Unix(), now.Add(-1*time.Hour).Unix(),
		"hash-vigente", userID, "csrf2", now.Unix(), now.Unix(), now.Add(1*time.Hour).Unix(),
	); err != nil {
		t.Fatal(err)
	}

	if err := m.CleanupExpired(context.Background()); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}

	var ids []string
	rows, err := db.Query(`SELECT id_hash FROM sessions ORDER BY id_hash`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 || ids[0] != "hash-vigente" {
		t.Errorf("esperado solo [hash-vigente], obtenido %v", ids)
	}
}
