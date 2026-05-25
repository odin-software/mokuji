package profile

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/session"
)

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := migrations.Run(db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func insertUser(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO users (email, created_at) VALUES (?, ?)`, email, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func setVisibility(t *testing.T, db *sql.DB, userID int64, vis string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE users SET list_visibility = ? WHERE id = ?`, vis, userID); err != nil {
		t.Fatal(err)
	}
}

func addEntry(t *testing.T, db *sql.DB, userID int64, title string) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO manga (source, source_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"anilist", "test-"+title, title, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at) VALUES (?, ?, 'reading', ?, ?)`,
		userID, mid, time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
}

func newTestHandler(t *testing.T, db *sql.DB) *Handler {
	t.Helper()
	return &Handler{
		db:     db,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func newFullHandler(t *testing.T, db *sql.DB) *Handler {
	t.Helper()
	h, err := New(Config{
		DB:            db,
		HTMLTemplates: os.DirFS("../../web/templates"),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// ----- /settings -----

func TestSettingsAnonymousRedirects(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	rec := httptest.NewRecorder()
	h.settings(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: esperado 303, obtenido %d", rec.Code)
	}
}

func TestUpdateSettingsValidatesVisibility(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")

	cases := []struct {
		name      string
		value     string
		wantCode  int
	}{
		{"public valido", "public", http.StatusSeeOther},
		{"private valido", "private", http.StatusSeeOther},
		{"invalido", "abierto", http.StatusBadRequest},
		{"vacio", "", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"list_visibility": {tc.value}}
			req := httptest.NewRequest(http.MethodPost, "/settings",
				strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req = session.WithUserID(req, userID)

			rec := httptest.NewRecorder()
			h.updateSettings(rec, req)

			if rec.Code != tc.wantCode {
				t.Errorf("status: esperado %d, obtenido %d", tc.wantCode, rec.Code)
			}
		})
	}
}

func TestUpdateSettingsPersists(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")

	form := url.Values{"list_visibility": {"private"}}
	req := httptest.NewRequest(http.MethodPost, "/settings",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = session.WithUserID(req, userID)

	rec := httptest.NewRecorder()
	h.updateSettings(rec, req)

	var vis string
	db.QueryRow(`SELECT list_visibility FROM users WHERE id = ?`, userID).Scan(&vis)
	if vis != "private" {
		t.Errorf("visibility no actualizada: %q", vis)
	}
}

func TestUpdateSettingsDigestFrequency(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	// El default de la migration es 'weekly'. Confirmamos punto de partida.
	var before string
	db.QueryRow(`SELECT digest_frequency FROM users WHERE id = ?`, userID).Scan(&before)
	if before != "weekly" {
		t.Fatalf("estado inicial esperado 'weekly', obtenido %q", before)
	}

	// Cambiar a 'daily'.
	form := url.Values{"list_visibility": {"public"}, "digest_frequency": {"daily"}}
	req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = session.WithUserID(req, userID)
	rec := httptest.NewRecorder()
	h.updateSettings(rec, req)

	var got string
	db.QueryRow(`SELECT digest_frequency FROM users WHERE id = ?`, userID).Scan(&got)
	if got != "daily" {
		t.Errorf("digest no se actualizó a 'daily': obtenido %q", got)
	}

	// Cambiar a 'off'.
	form2 := url.Values{"list_visibility": {"public"}, "digest_frequency": {"off"}}
	req2 := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form2.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2 = session.WithUserID(req2, userID)
	h.updateSettings(httptest.NewRecorder(), req2)

	db.QueryRow(`SELECT digest_frequency FROM users WHERE id = ?`, userID).Scan(&got)
	if got != "off" {
		t.Errorf("digest no se actualizó a 'off': obtenido %q", got)
	}

	// Valor inválido cae a 'weekly' (safe default).
	form3 := url.Values{"list_visibility": {"public"}, "digest_frequency": {"bogus"}}
	req3 := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form3.Encode()))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req3 = session.WithUserID(req3, userID)
	h.updateSettings(httptest.NewRecorder(), req3)

	db.QueryRow(`SELECT digest_frequency FROM users WHERE id = ?`, userID).Scan(&got)
	if got != "weekly" {
		t.Errorf("valor inválido no cayó a 'weekly': obtenido %q", got)
	}
}

// ----- /u/{id} -----

func TestPublicProfileVisibleWhenPublic(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	setVisibility(t, db, userID, "public")
	addEntry(t, db, userID, "Berserk")

	req := httptest.NewRequest(http.MethodGet, "/u/"+strconv.FormatInt(userID, 10), nil)
	req.SetPathValue("handle", strconv.FormatInt(userID, 10))
	rec := httptest.NewRecorder()
	h.publicProfile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Berserk") {
		t.Error("body no contiene Berserk")
	}
}

func TestPublicProfile404WhenPrivateAndAnonymous(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	setVisibility(t, db, userID, "private")
	addEntry(t, db, userID, "Secret Berserk")

	req := httptest.NewRequest(http.MethodGet, "/u/"+strconv.FormatInt(userID, 10), nil)
	req.SetPathValue("handle", strconv.FormatInt(userID, 10))
	rec := httptest.NewRecorder()
	h.publicProfile(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: esperado 404, obtenido %d", rec.Code)
	}
	// No debería filtrar contenido del body
	if strings.Contains(rec.Body.String(), "Secret Berserk") {
		t.Error("body filtró título de lista privada")
	}
}

func TestPublicProfileVisibleToOwnerWhenPrivate(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	setVisibility(t, db, userID, "private")
	addEntry(t, db, userID, "Berserk")

	req := httptest.NewRequest(http.MethodGet, "/u/"+strconv.FormatInt(userID, 10), nil)
	req.SetPathValue("handle", strconv.FormatInt(userID, 10))
	req = session.WithUserID(req, userID)
	rec := httptest.NewRecorder()
	h.publicProfile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Berserk") {
		t.Error("owner no ve su propia lista privada")
	}
}

func TestPublicProfile404WhenPrivateAndOtherUser(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db)
	alice := insertUser(t, db, "alice@example.com")
	bob := insertUser(t, db, "bob@example.com")
	setVisibility(t, db, alice, "private")
	addEntry(t, db, alice, "Berserk")

	req := httptest.NewRequest(http.MethodGet, "/u/"+strconv.FormatInt(alice, 10), nil)
	req.SetPathValue("handle", strconv.FormatInt(alice, 10))
	req = session.WithUserID(req, bob)
	rec := httptest.NewRecorder()
	h.publicProfile(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("bob debería recibir 404, obtenido %d", rec.Code)
	}
}

func TestPublicProfile404ForUnknownUser(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)

	req := httptest.NewRequest(http.MethodGet, "/u/9999", nil)
	req.SetPathValue("handle", "9999")
	rec := httptest.NewRecorder()
	h.publicProfile(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: esperado 404, obtenido %d", rec.Code)
	}
}

func TestPublicProfileHidesSourceURLAndNotes(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	setVisibility(t, db, userID, "public")

	// Insertar entry con source_url y notes
	res, _ := db.Exec(`INSERT INTO manga (source, source_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"anilist", "x", "Berserk", time.Now().Unix(), time.Now().Unix())
	mid, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO library_entries (user_id, manga_id, status, source_url, notes, created_at, updated_at) VALUES (?, ?, 'reading', ?, ?, ?, ?)`,
		userID, mid, "https://privadafuente.example/foo", "spoilers privados",
		time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/u/"+strconv.FormatInt(userID, 10), nil)
	req.SetPathValue("handle", strconv.FormatInt(userID, 10))
	rec := httptest.NewRecorder()
	h.publicProfile(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "privadafuente.example") {
		t.Error("body expone source_url")
	}
	if strings.Contains(body, "spoilers privados") {
		t.Error("body expone notes")
	}
}

func TestCountByStatus(t *testing.T) {
	cases := []struct {
		name    string
		entries []ProfileEntry
		want    *profileCounters
	}{
		{"vacío", nil, nil},
		{
			"mezcla",
			[]ProfileEntry{
				{Status: "reading"},
				{Status: "reading"},
				{Status: "planned"},
				{Status: "completed"},
				{Status: "completed"},
				{Status: "completed"},
				{Status: "weird"}, // estados desconocidos se ignoran sin romper
			},
			&profileCounters{Reading: 2, Planned: 1, Completed: 3},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := countByStatus(tc.entries)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("nilness: esperado %v, obtenido %v", tc.want, got)
			}
			if got != nil && *got != *tc.want {
				t.Errorf("counters: esperado %+v, obtenido %+v", *tc.want, *got)
			}
		})
	}
}
