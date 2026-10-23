package server

import (
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
)

// walkTemplates llama fn con el path y el contenido de cada .html de
// web/templates.
func walkTemplates(t *testing.T, fn func(path, body string)) {
	t.Helper()
	fsys := os.DirFS("../../web/templates")
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		b, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		fn(path, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// La CSP (script-src 'self') bloquea los handlers inline sin avisar: el
// confirm() de borrar estuvo muerto en producción por esto.
func TestTemplatesNoInlineHandlers(t *testing.T) {
	re := regexp.MustCompile(`\son[a-z]+=`)
	walkTemplates(t, func(path, body string) {
		if loc := re.FindStringIndex(body); loc != nil {
			t.Errorf("%s: handler inline %q", path, body[loc[0]:loc[1]+20])
		}
	})
}

// Los kanji grandes de los empty states y de las páginas de error son
// decoración: sin aria-hidden el lector los lee como caracteres sueltos.
func TestTemplatesDecorativeGlyphsHidden(t *testing.T) {
	re := regexp.MustCompile(`<[a-z]+[^>]*class="(kanji|error-glyph[^"]*)"[^>]*>`)
	walkTemplates(t, func(path, body string) {
		for _, tag := range re.FindAllString(body, -1) {
			if !strings.Contains(tag, `aria-hidden="true"`) {
				t.Errorf("%s: glifo decorativo sin aria-hidden: %s", path, tag)
			}
		}
	})
}
