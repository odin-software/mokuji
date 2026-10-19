package library

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"git.odin.do/kengru/mokuji/internal/flash"
	"git.odin.do/kengru/mokuji/internal/mangasource"
	"git.odin.do/kengru/mokuji/internal/session"
)

type fakeResolver struct {
	found map[int]mangasource.SearchResult
	err   error
	calls int
}

func (f *fakeResolver) ByMALIDs(_ context.Context, ids []int) (map[int]mangasource.SearchResult, error) {
	f.calls++
	return f.found, f.err
}

func newImportHandler(t *testing.T, res MALResolver) (*Handler, int64, func() int) {
	t.Helper()
	db := setupDB(t)
	h, err := New(Config{
		DB:            db,
		MangaSource:   &fakeSource{},
		HTMLTemplates: os.DirFS("../../web/templates"),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		MALResolver:   res,
		Flasher:       flash.New(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	user := insertUser(t, db, "alice@example.com")
	return h, user, func() int { return countEntries(t, db, user) }
}

func importReq(t *testing.T, userID int64, file string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "mangalist.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(fw, file); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/settings/import/mal", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req.WithContext(session.NewContext(context.Background(), &session.Session{UserID: userID}))
}

// flashMessage devuelve el mensaje del flash que dejó la respuesta.
func flashMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "_mokuji_flash" {
			_, msg, _ := strings.Cut(c.Value, "|")
			m, err := url.QueryUnescape(msg)
			if err != nil {
				t.Fatal(err)
			}
			return m
		}
	}
	return ""
}

const twoMangaExport = `<myanimelist>
<manga><manga_mangadb_id>2</manga_mangadb_id><manga_title>Berserk</manga_title><my_read_chapters>380</my_read_chapters><my_status>Reading</my_status></manga>
<manga><manga_mangadb_id>99</manga_mangadb_id><manga_title>Doujin raro</manga_title><my_read_chapters>0</my_read_chapters><my_status>Plan to Read</my_status></manga>
</myanimelist>`

func TestImportHandlerRendersSummary(t *testing.T) {
	h, user, count := newImportHandler(t, &fakeResolver{found: foundFor(2)})

	rec := httptest.NewRecorder()
	h.importMAL(rec, importReq(t, user, twoMangaExport))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: esperado 200, obtenido %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Doujin raro", `href="/library"`, "los capítulos de lo que estás leyendo van a ir apareciendo"} {
		if !strings.Contains(body, want) {
			t.Errorf("el resumen no contiene %q", want)
		}
	}
	if n := count(); n != 1 {
		t.Errorf("entries: esperado 1, obtenido %d", n)
	}
}

func TestImportHandlerAnimeExportFlashesAndRedirects(t *testing.T) {
	res := &fakeResolver{}
	h, user, _ := newImportHandler(t, res)

	rec := httptest.NewRecorder()
	h.importMAL(rec, importReq(t, user, `<myanimelist><anime><series_animedb_id>1</series_animedb_id></anime></myanimelist>`))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings" {
		t.Fatalf("esperado 303 a /settings, obtenido %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := flashMessage(t, rec); got != "ese es tu export de anime; necesitamos el de manga." {
		t.Errorf("flash: %q", got)
	}
	if res.calls != 0 {
		t.Error("no debería consultar AniList con un export inválido")
	}
}

func TestImportHandlerResolverErrorWritesNothing(t *testing.T) {
	h, user, count := newImportHandler(t, &fakeResolver{err: errors.New("anilist status 429")})

	rec := httptest.NewRecorder()
	h.importMAL(rec, importReq(t, user, twoMangaExport))

	if got := flashMessage(t, rec); got != "AniList está ocupado o no responde. intenta en unos minutos." {
		t.Errorf("flash: %q", got)
	}
	if n := count(); n != 0 {
		t.Errorf("no debería escribir nada si AniList falla; entries: %d", n)
	}
}

func TestImportHandlerRateLimited(t *testing.T) {
	h, user, _ := newImportHandler(t, &fakeResolver{found: foundFor(2)})

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.importMAL(rec, importReq(t, user, twoMangaExport))
		if rec.Code != http.StatusOK {
			t.Fatalf("import %d: esperado 200, obtenido %d", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.importMAL(rec, importReq(t, user, twoMangaExport))
	if got := flashMessage(t, rec); got != "ya importaste varias veces seguidas. intenta en un rato." {
		t.Errorf("cuarto import: flash %q (status %d)", got, rec.Code)
	}
}

func TestImportRouteNotMountedWithoutResolver(t *testing.T) {
	h := newFullHandler(t, setupDB(t), nil)
	mux := http.NewServeMux()
	h.Mount(mux)
	if _, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, "/settings/import/mal", nil)); pattern != "" {
		t.Errorf("la ruta no debería montarse sin MALResolver, matcheó %q", pattern)
	}
}
