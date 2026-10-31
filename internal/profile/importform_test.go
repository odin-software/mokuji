package profile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/odin-software/mokuji/internal/session"
)

func TestSettingsHasMALImportForm(t *testing.T) {
	db := setupDB(t)
	h := newFullHandler(t, db)
	userID := insertUser(t, db, "alice@example.com")

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID}))
	rec := httptest.NewRecorder()
	h.settings(rec, req)

	body := rec.Body.String()
	for _, want := range []string{`action="/settings/import/mal"`, `enctype="multipart/form-data"`, `https://myanimelist.net/panel.php?go=export`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings no contiene %q", want)
		}
	}
	// El input de archivo tiene un label asociado por for/id.
	m := regexp.MustCompile(`<input[^>]*type="file"[^>]*id="([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no hay input type=file con id")
	}
	if !strings.Contains(body, `<label for="`+m[1]+`"`) {
		t.Errorf("el input de archivo %q no tiene label asociado", m[1])
	}
}
