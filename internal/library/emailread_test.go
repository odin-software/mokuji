package library

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.odin.do/kengru/mokuji/internal/unsubscribe"
)

func newHandlerWithSigner(t *testing.T, db *sql.DB) (*Handler, *unsubscribe.Signer) {
	t.Helper()
	signer, err := unsubscribe.NewSigner(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	h := newTestHandler(t, db, nil)
	h.signer = signer
	return h, signer
}

func TestEmailReadNotMountedWithoutSigner(t *testing.T) {
	db := setupDB(t)
	h := newTestHandler(t, db, nil)
	if h.signer != nil {
		t.Fatal("test handler shouldn't have signer cabled")
	}

	r := httptest.NewRequest("GET", "/r/abc", nil)
	r.SetPathValue("token", "abc")
	w := httptest.NewRecorder()
	h.emailRead(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: %d (esperaba 404 cuando signer es nil)", w.Code)
	}
}

func TestEmailReadInvalidToken(t *testing.T) {
	db := setupDB(t)
	h, _ := newHandlerWithSigner(t, db)

	r := httptest.NewRequest("GET", "/r/basura", nil)
	r.SetPathValue("token", "basura")
	w := httptest.NewRecorder()
	h.emailRead(w, r)

	if w.Code != http.StatusGone {
		t.Errorf("status: %d (esperaba 410)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "link") {
		t.Errorf("body sin mensaje de error: %s", w.Body.String())
	}
}

func TestEmailReadExpiredToken(t *testing.T) {
	db := setupDB(t)
	h, signer := newHandlerWithSigner(t, db)

	// Forzamos un token "viejo" reseteando el clock del signer.
	old := time.Now().Add(-31 * 24 * time.Hour)
	signer.SetNowForTesting(func() time.Time { return old })
	tok := signer.SignReadChapter(1, 1)
	signer.SetNowForTesting(time.Now)

	r := httptest.NewRequest("GET", "/r/"+tok, nil)
	r.SetPathValue("token", tok)
	w := httptest.NewRecorder()
	h.emailRead(w, r)

	if w.Code != http.StatusGone {
		t.Errorf("status: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "expir") {
		t.Errorf("mensaje de expirado ausente: %s", w.Body.String())
	}
}

func TestEmailReadSuccessAdvancesCursorAndRedirects(t *testing.T) {
	db := setupDB(t)
	h, signer := newHandlerWithSigner(t, db)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	chID := insertChapter(t, db, mangaID, "ch-1", "1", "en", "https://reader.example/x", time.Now().Add(-1*time.Hour))

	tok := signer.SignReadChapter(uid, chID)
	r := httptest.NewRequest("GET", "/r/"+tok, nil)
	r.SetPathValue("token", tok)
	w := httptest.NewRecorder()
	h.emailRead(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `meta http-equiv="refresh"`) {
		t.Errorf("falta meta refresh: %s", body)
	}
	if !strings.Contains(body, "https://reader.example/x") {
		t.Errorf("URL externa ausente: %s", body)
	}

	var cur sql.NullInt64
	if err := db.QueryRow(`SELECT last_read_chapter_id FROM library_entries WHERE id = ?`, entryID).Scan(&cur); err != nil {
		t.Fatal(err)
	}
	if !cur.Valid || cur.Int64 != chID {
		t.Errorf("cursor: esperado %d, obtenido %v", chID, cur)
	}
}

func TestEmailReadFallsBackToMangadexURL(t *testing.T) {
	db := setupDB(t)
	h, signer := newHandlerWithSigner(t, db)
	uid := insertUser(t, db, "alice@example.com")
	_, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	chID := insertChapter(t, db, mangaID, "abc-uuid", "1", "en", "", time.Now().Add(-1*time.Hour))

	tok := signer.SignReadChapter(uid, chID)
	r := httptest.NewRequest("GET", "/r/"+tok, nil)
	r.SetPathValue("token", tok)
	w := httptest.NewRecorder()
	h.emailRead(w, r)

	if !strings.Contains(w.Body.String(), "https://mangadex.org/chapter/abc-uuid") {
		t.Errorf("fallback URL ausente: %s", w.Body.String())
	}
}

func TestEmailReadRejectsOtherUserChapter(t *testing.T) {
	db := setupDB(t)
	h, signer := newHandlerWithSigner(t, db)
	alice := insertUser(t, db, "alice@example.com")
	bob := insertUser(t, db, "bob@example.com")
	_, bobMangaID := insertMangaWithChapters(t, db, bob, "matched")
	bobChID := insertChapter(t, db, bobMangaID, "ch-bob", "1", "en", "", time.Now().Add(-1*time.Hour))

	// Token firmado para Alice + chapter de Bob → cadena de joins no produce
	// filas → handler responde "inválido".
	tok := signer.SignReadChapter(alice, bobChID)
	r := httptest.NewRequest("GET", "/r/"+tok, nil)
	r.SetPathValue("token", tok)
	w := httptest.NewRecorder()
	h.emailRead(w, r)

	if w.Code != http.StatusGone {
		t.Errorf("status: %d (esperaba 410)", w.Code)
	}
}

func TestEmailReadRejectsSoftDeletedUser(t *testing.T) {
	db := setupDB(t)
	h, signer := newHandlerWithSigner(t, db)
	uid := insertUser(t, db, "alice@example.com")
	_, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	chID := insertChapter(t, db, mangaID, "ch-x", "1", "en", "", time.Now().Add(-1*time.Hour))
	if _, err := db.Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, time.Now().Unix(), uid); err != nil {
		t.Fatal(err)
	}

	tok := signer.SignReadChapter(uid, chID)
	r := httptest.NewRequest("GET", "/r/"+tok, nil)
	r.SetPathValue("token", tok)
	w := httptest.NewRecorder()
	h.emailRead(w, r)

	if w.Code != http.StatusGone {
		t.Errorf("status: %d (esperaba 410 para user soft-deleted)", w.Code)
	}
}

func TestEmailReadRejectsNonReadingStatus(t *testing.T) {
	db := setupDB(t)
	h, signer := newHandlerWithSigner(t, db)
	uid := insertUser(t, db, "alice@example.com")
	entryID, mangaID := insertMangaWithChapters(t, db, uid, "matched")
	chID := insertChapter(t, db, mangaID, "ch-x", "1", "en", "", time.Now().Add(-1*time.Hour))
	if _, err := db.Exec(`UPDATE library_entries SET status = 'completed' WHERE id = ?`, entryID); err != nil {
		t.Fatal(err)
	}

	tok := signer.SignReadChapter(uid, chID)
	r := httptest.NewRequest("GET", "/r/"+tok, nil)
	r.SetPathValue("token", tok)
	w := httptest.NewRecorder()
	h.emailRead(w, r)

	if w.Code != http.StatusGone {
		t.Errorf("status: %d (status != reading no debe consumir el link)", w.Code)
	}
}
