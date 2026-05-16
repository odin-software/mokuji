package code

import (
	"testing"
)

func TestGenerateProduces6Digits(t *testing.T) {
	seen := map[string]int{}
	for range 100 {
		c, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if !Valid(c) {
			t.Fatalf("código inválido: %q", c)
		}
		seen[c]++
	}
	if len(seen) < 95 {
		t.Errorf("baja entropía: %d únicos de 100", len(seen))
	}
}

func TestValid(t *testing.T) {
	cases := map[string]bool{
		"123456":  true,
		"000000":  true,
		"12345":   false,
		"1234567": false,
		"12345a":  false,
		"":        false,
		" 12345":  false,
	}
	for in, want := range cases {
		if got := Valid(in); got != want {
			t.Errorf("Valid(%q): esperado %v, obtenido %v", in, want, got)
		}
	}
}

func TestFormat(t *testing.T) {
	if got := Format("123456"); got != "123 456" {
		t.Errorf("Format: esperado %q, obtenido %q", "123 456", got)
	}
	if got := Format("12345"); got != "12345" {
		t.Errorf("Format con longitud rara debería pasar: %q", got)
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"123456", "123456"},
		{"  123456 ", "123456"},
		{"123 456", "123456"},
		{"  123 456  ", "123456"},
		// NBSP (U+00A0) escapado a propósito: Gmail/Apple Mail lo meten al
		// pegar; si lo escribimos como espacio normal el editor lo colapsa.
		{"123 456", "123456"},
		{"1\t2\n3 4-5-6", "123456"},
		{"abc", ""},
		{"", ""},
		{"123456abc", "123456"},
	}
	for _, tc := range cases {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q): esperado %q, obtenido %q", tc.in, tc.want, got)
		}
	}
}

func TestHashIsConsistent(t *testing.T) {
	a := Hash("123456")
	b := Hash("123456")
	if a != b {
		t.Error("Hash no es determinista")
	}
	if a == Hash("123457") {
		t.Error("Hash colisiona en inputs distintos")
	}
}
