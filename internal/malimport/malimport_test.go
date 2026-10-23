package malimport

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func readFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/mangalist.xml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var fixtureWant = []Entry{
	{MALID: 2, Title: "Berserk", ReadChapters: 380, Status: "reading"},
	{MALID: 13, Title: "One Piece", ReadChapters: 0, Status: "planned"},
	{MALID: 1706, Title: "JoJo no Kimyou na Bouken Part 7: Steel Ball Run", ReadChapters: 95, Status: "completed"},
	{MALID: 656, Title: "Vagabond", ReadChapters: 120, Status: "planned"},
	{MALID: 11, Title: "Naruto", ReadChapters: 40, Status: ""},
}

func TestParseFixture(t *testing.T) {
	got, err := Parse(bytes.NewReader(readFixture(t)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(got, fixtureWant) {
		t.Errorf("entries:\n got  %+v\n want %+v", got, fixtureWant)
	}
}

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseGzip(t *testing.T) {
	got, err := Parse(bytes.NewReader(gzipBytes(t, readFixture(t))))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(got, fixtureWant) {
		t.Errorf("entries:\n got  %+v\n want %+v", got, fixtureWant)
	}
}

func TestMapStatus(t *testing.T) {
	cases := map[string]string{
		"Reading":      "reading",
		"Plan to Read": "planned",
		"Completed":    "completed",
		"On-Hold":      "planned",
		"Dropped":      "",
		"":             "planned",
		"foo":          "planned",
	}
	for in, want := range cases {
		if got := MapStatus(in); got != want {
			t.Errorf("MapStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func oneEntry(readChapters string) string {
	return `<myanimelist><manga><manga_mangadb_id>2</manga_mangadb_id>` +
		`<manga_title>Berserk</manga_title><my_read_chapters>` + readChapters +
		`</my_read_chapters><my_status>Reading</my_status></manga></myanimelist>`
}

func TestParseReadChaptersGarbage(t *testing.T) {
	for _, raw := range []string{"", "abc", "-3"} {
		got, err := Parse(strings.NewReader(oneEntry(raw)))
		if err != nil {
			t.Fatalf("%q: Parse: %v", raw, err)
		}
		if len(got) != 1 || got[0].ReadChapters != 0 {
			t.Errorf("%q: esperado ReadChapters 0, obtenido %+v", raw, got)
		}
	}
}

func TestParseAnimeExport(t *testing.T) {
	anime := `<myanimelist><myinfo><user_export_type>1</user_export_type></myinfo>` +
		`<anime><series_animedb_id>1</series_animedb_id></anime></myanimelist>`
	if _, err := Parse(strings.NewReader(anime)); !errors.Is(err, ErrAnimeExport) {
		t.Errorf("esperado ErrAnimeExport, obtenido %v", err)
	}
}

func TestParseNotMAL(t *testing.T) {
	for name, doc := range map[string]string{
		"otro xml":         `<library><book>x</book></library>`,
		"mal sin entradas": `<myanimelist><myinfo></myinfo></myanimelist>`,
		"no es xml":        `hola`,
	} {
		if _, err := Parse(strings.NewReader(doc)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: esperado ErrInvalid, obtenido %v", name, err)
		}
	}
}

func TestParseTooManyEntries(t *testing.T) {
	var b strings.Builder
	b.WriteString("<myanimelist>")
	for i := 1; i <= MaxEntries+1; i++ {
		fmt.Fprintf(&b, `<manga><manga_mangadb_id>%d</manga_mangadb_id><manga_title>m</manga_title>`+
			`<my_read_chapters>0</my_read_chapters><my_status>Reading</my_status></manga>`, i)
	}
	b.WriteString("</myanimelist>")
	if _, err := Parse(strings.NewReader(b.String())); !errors.Is(err, ErrTooManyEntries) {
		t.Errorf("esperado ErrTooManyEntries, obtenido %v", err)
	}
}

func TestParseGzipBomb(t *testing.T) {
	doc := "<myanimelist>" + strings.Repeat(" ", 21<<20) + "</myanimelist>"
	bomb := gzipBytes(t, []byte(doc))
	if len(bomb) > 1<<20 {
		t.Fatalf("el bomb debería ser chico comprimido, mide %d", len(bomb))
	}
	if _, err := Parse(bytes.NewReader(bomb)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("esperado ErrTooLarge, obtenido %v", err)
	}
}

// Export real de anime de MAL (recortado y anonimizado): confirma que el
// formato de verdad cae en ErrAnimeExport y no en un "0 importados".
func TestParseRealAnimeExport(t *testing.T) {
	b, err := os.ReadFile("testdata/animelist_real.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(bytes.NewReader(gzipBytes(t, b))); !errors.Is(err, ErrAnimeExport) {
		t.Errorf("esperado ErrAnimeExport, obtenido %v", err)
	}
}
