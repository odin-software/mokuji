package profile

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.odin.do/kengru/mokuji/internal/session"
)

func TestExportRequiresSession(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)

	req := httptest.NewRequest(http.MethodGet, "/settings/export", nil)
	rec := httptest.NewRecorder()
	h.exportData(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: esperado 303, obtenido %d", rec.Code)
	}
}

func TestExportShape(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")
	addEntry(t, db, userID, "Berserk")

	req := httptest.NewRequest(http.MethodGet, "/settings/export", nil)
	req = session.WithUserID(req, userID)
	rec := httptest.NewRecorder()
	h.exportData(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "mokuji-export-") {
		t.Errorf("Content-Disposition: %q", cd)
	}

	var data exportData
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatalf("parse JSON: %v\nbody: %s", err, rec.Body.String())
	}
	if data.FormatVersion != exportFormatVersion {
		t.Errorf("format_version: %d", data.FormatVersion)
	}
	if data.Profile.ID != userID {
		t.Errorf("profile.id: %d", data.Profile.ID)
	}
	if data.Profile.Email != "alice@example.com" {
		t.Errorf("profile.email: %q", data.Profile.Email)
	}
	if len(data.Library) != 1 {
		t.Fatalf("library entries: %d", len(data.Library))
	}
	if data.Library[0].Manga.Title != "Berserk" {
		t.Errorf("manga title: %q", data.Library[0].Manga.Title)
	}
	if data.ExportedAt.IsZero() {
		t.Error("exported_at vacío")
	}
}

func TestExportIncludesPrivateFields(t *testing.T) {
	// El export es para el dueño — debe incluir source_url y notes
	// (a diferencia de /u/{id} que las oculta a terceros).
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")

	res, _ := db.Exec(`INSERT INTO manga (source, source_id, title, created_at, updated_at)
		VALUES ('anilist', 'mid-x', 'Berserk', ?, ?)`, time.Now().Unix(), time.Now().Unix())
	mid, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO library_entries
		(user_id, manga_id, status, source_url, notes, created_at, updated_at)
		VALUES (?, ?, 'reading', 'https://mireader.example/x', 'spoilers privados', ?, ?)`,
		userID, mid, time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/export", nil)
	req = session.WithUserID(req, userID)
	rec := httptest.NewRecorder()
	h.exportData(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "mireader.example") {
		t.Error("export no incluye source_url propio")
	}
	if !strings.Contains(body, "spoilers privados") {
		t.Error("export no incluye notes propios")
	}
}

func TestExportEmptyLibrary(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")

	req := httptest.NewRequest(http.MethodGet, "/settings/export", nil)
	req = session.WithUserID(req, userID)
	rec := httptest.NewRecorder()
	h.exportData(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var data exportData
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Library) != 0 {
		t.Errorf("library debería ser vacía: %v", data.Library)
	}
}
