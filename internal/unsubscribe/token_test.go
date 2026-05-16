package unsubscribe

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func newTestSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := NewSigner(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewSignerRejectsShortSecret(t *testing.T) {
	if _, err := NewSigner(make([]byte, 31)); err == nil {
		t.Error("se aceptó un secret < 32 bytes")
	}
	if _, err := NewSigner(nil); err == nil {
		t.Error("se aceptó secret nil")
	}
}

func TestSignVerifyRoundtrip(t *testing.T) {
	s := newTestSigner(t)
	tok := s.Sign(42, ScopeReleasesDigest)
	uid, err := s.Verify(tok, ScopeReleasesDigest)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if uid != 42 {
		t.Errorf("userID: esperado 42, obtenido %d", uid)
	}
}

func TestVerifyRejectsTampered(t *testing.T) {
	s := newTestSigner(t)
	tok := s.Sign(42, ScopeReleasesDigest)

	// Tamper en payload: cambia un byte del base64 del payload.
	dot := strings.IndexByte(tok, '.')
	tampered := flipChar(tok[:dot]) + tok[dot:]
	if _, err := s.Verify(tampered, ScopeReleasesDigest); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("payload tampered aceptado: %v", err)
	}

	// Tamper en signature: cambia un byte del base64 de la firma.
	tampered = tok[:dot+1] + flipChar(tok[dot+1:])
	if _, err := s.Verify(tampered, ScopeReleasesDigest); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("sig tampered aceptado: %v", err)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	s1, _ := NewSigner(bytes32('a'))
	s2, _ := NewSigner(bytes32('b'))

	tok := s1.Sign(42, ScopeReleasesDigest)
	if _, err := s2.Verify(tok, ScopeReleasesDigest); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("token firmado con otro secret aceptado: %v", err)
	}
}

func TestVerifyRejectsWrongScope(t *testing.T) {
	s := newTestSigner(t)
	tok := s.Sign(42, ScopeReleasesDigest)
	if _, err := s.Verify(tok, "otro_scope"); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("scope incorrecto aceptado: %v", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	s := newTestSigner(t)
	// Firmamos "hace 100 días", verificamos "ahora".
	old := time.Now().Add(-100 * 24 * time.Hour)
	s.now = func() time.Time { return old }
	tok := s.Sign(42, ScopeReleasesDigest)
	s.now = time.Now

	if _, err := s.Verify(tok, ScopeReleasesDigest); !errors.Is(err, ErrExpiredToken) {
		t.Errorf("token expirado aceptado: %v", err)
	}
}

func TestVerifyAcceptsRecentToken(t *testing.T) {
	s := newTestSigner(t)
	// Justo dentro de la ventana (89 días).
	old := time.Now().Add(-89 * 24 * time.Hour)
	s.now = func() time.Time { return old }
	tok := s.Sign(42, ScopeReleasesDigest)
	s.now = time.Now

	if _, err := s.Verify(tok, ScopeReleasesDigest); err != nil {
		t.Errorf("token reciente rechazado: %v", err)
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	s := newTestSigner(t)
	cases := []string{
		"",
		"sin-punto",
		".",
		"a.b.c",                  // demasiados separadores en payload, pero el split sigue
		"!!!.!!!",                // base64 inválido
		"YQ.YQ",                  // payload válido base64 pero no parsea como userID:scope:ts
		"YQ==.YQ==",              // padding (rejected por RawURLEncoding)
	}
	for _, in := range cases {
		if _, err := s.Verify(in, ScopeReleasesDigest); err == nil {
			t.Errorf("token malformado aceptado: %q", in)
		}
	}
}

func TestSignVerifyReadChapterRoundtrip(t *testing.T) {
	s := newTestSigner(t)
	tok := s.SignReadChapter(42, 1234)
	uid, cid, err := s.VerifyReadChapter(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if uid != 42 || cid != 1234 {
		t.Errorf("ids: got userID=%d chapterID=%d", uid, cid)
	}
}

func TestVerifyReadChapterRejectsCrossScopeToken(t *testing.T) {
	// Token de unsubscribe NO debe valer para read_chapter (distinto payload
	// → la firma no matchea cuando hmac re-firma con el formato de read).
	s := newTestSigner(t)
	tok := s.Sign(42, ScopeReleasesDigest)
	if _, _, err := s.VerifyReadChapter(tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("token de unsubscribe aceptado como read_chapter: %v", err)
	}
}

func TestVerifyReadChapterRejectsExpired(t *testing.T) {
	s := newTestSigner(t)
	old := time.Now().Add(-31 * 24 * time.Hour)
	s.now = func() time.Time { return old }
	tok := s.SignReadChapter(42, 1234)
	s.now = time.Now
	if _, _, err := s.VerifyReadChapter(tok); !errors.Is(err, ErrExpiredToken) {
		t.Errorf("token expirado aceptado: %v", err)
	}
}

func TestVerifyReadChapterAcceptsRecent(t *testing.T) {
	s := newTestSigner(t)
	old := time.Now().Add(-29 * 24 * time.Hour)
	s.now = func() time.Time { return old }
	tok := s.SignReadChapter(42, 1234)
	s.now = time.Now
	if _, _, err := s.VerifyReadChapter(tok); err != nil {
		t.Errorf("token reciente rechazado: %v", err)
	}
}

func TestVerifyReadChapterRejectsTampered(t *testing.T) {
	s := newTestSigner(t)
	tok := s.SignReadChapter(42, 1234)
	// Tamper en el medio de la firma — flipear el último char puede caer
	// en bits de padding que RawURLEncoding ignora.
	dot := strings.IndexByte(tok, '.')
	mid := dot + 1 + (len(tok)-dot-1)/2
	tampered := tok[:mid] + flipByte(tok[mid]) + tok[mid+1:]
	if _, _, err := s.VerifyReadChapter(tampered); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("sig tampered aceptado: %v", err)
	}
}

// flipByte cambia b por 'A' (si ya era 'A', por 'B'). Garantiza que el byte
// resultante sea distinto y siga siendo válido en base64url.
func flipByte(b byte) string {
	if b == 'A' {
		return "B"
	}
	return "A"
}

// flipChar cambia el último caracter del input por '_' (válido en base64url),
// o por 'A' si ya era '_', garantizando que el output difiera del input.
func flipChar(s string) string {
	if s == "" {
		return "A"
	}
	last := s[len(s)-1]
	repl := byte('_')
	if last == '_' {
		repl = 'A'
	}
	return s[:len(s)-1] + string(repl)
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}
