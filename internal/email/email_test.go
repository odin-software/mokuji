package email

import (
	"errors"
	"io"
	"log/slog"
	"net/smtp"
	"strings"
	"testing"
)

func newTestClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{
		Host:   "localhost",
		Port:   1025,
		From:   "mokuji <auth@mokuji.local>",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewRejectsBadFrom(t *testing.T) {
	_, err := New(Config{Host: "h", Port: 25, From: "no-es-email"})
	if err == nil {
		t.Error("se esperaba error para From inválido")
	}
}

func TestNewRejectsEmptyConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"sin host", Config{Port: 25, From: "a@b.com"}},
		{"sin port", Config{Host: "h", From: "a@b.com"}},
		{"sin from", Config{Host: "h", Port: 25}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg); err == nil {
				t.Error("se esperaba error")
			}
		})
	}
}

func TestBuildMessageBasic(t *testing.T) {
	c := newTestClient(t)
	msg := string(c.buildMessage("user@example.com", "Hola", "Cuerpo"))

	wants := []string{
		"From: mokuji <auth@mokuji.local>\r\n",
		"To: user@example.com\r\n",
		"Subject: Hola\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"Content-Transfer-Encoding: 8bit\r\n",
	}
	for _, w := range wants {
		if !strings.Contains(msg, w) {
			t.Errorf("falta línea: %q", w)
		}
	}

	if !strings.Contains(msg, "\r\n\r\nCuerpo") {
		t.Errorf("separator headers/body incorrecto:\n%s", msg)
	}

	// Date y Message-ID deben existir aunque no chequeamos contenido exacto.
	if !strings.Contains(msg, "\r\nDate: ") {
		t.Error("falta header Date")
	}
	if !strings.Contains(msg, "\r\nMessage-ID: <") || !strings.Contains(msg, "@mokuji.local>") {
		t.Error("falta header Message-ID con dominio del From")
	}
}

func TestBuildMessageEncodesUTF8Subject(t *testing.T) {
	c := newTestClient(t)
	msg := string(c.buildMessage("user@example.com", "Tu código de mokuji", "x"))

	if !strings.Contains(msg, "=?utf-8?") {
		t.Errorf("subject con acentos no fue Q-encoded:\n%s", msg)
	}
	if strings.Contains(msg, "Subject: Tu código de mokuji") {
		t.Error("subject debió ser Q-encoded, fue insertado plain")
	}
}

func TestBuildMessageNormalizesLineEndings(t *testing.T) {
	c := newTestClient(t)
	msg := string(c.buildMessage("user@example.com", "S", "linea1\nlinea2\r\nlinea3"))

	_, body, ok := strings.Cut(msg, "\r\n\r\n")
	if !ok {
		t.Fatal("no se encontró separator")
	}
	want := "linea1\r\nlinea2\r\nlinea3"
	if body != want {
		t.Errorf("line endings:\nesperado %q\nobtenido %q", want, body)
	}
}

func TestSendCallsSMTPWithExpectedArgs(t *testing.T) {
	c := newTestClient(t)

	var captured struct {
		addr string
		from string
		to   []string
		msg  []byte
	}
	c.sendMail = func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
		captured.addr = addr
		captured.from = from
		captured.to = to
		captured.msg = msg
		return nil
	}

	if err := c.Send("user@example.com", "Hola", "Cuerpo"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if captured.addr != "localhost:1025" {
		t.Errorf("addr: esperado localhost:1025, obtenido %q", captured.addr)
	}
	if captured.from != "auth@mokuji.local" {
		t.Errorf("from envelope: esperado auth@mokuji.local, obtenido %q", captured.from)
	}
	if len(captured.to) != 1 || captured.to[0] != "user@example.com" {
		t.Errorf("to: esperado [user@example.com], obtenido %v", captured.to)
	}
	if !strings.Contains(string(captured.msg), "Subject: Hola") {
		t.Errorf("msg no contiene subject: %q", string(captured.msg))
	}
}

func TestSendRejectsBadTo(t *testing.T) {
	c := newTestClient(t)
	c.sendMail = func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
		t.Fatal("sendMail no debería ser llamado")
		return nil
	}
	if err := c.Send("no-es-email", "S", "B"); err == nil {
		t.Error("se esperaba error para To inválido")
	}
}

func TestSendPropagatesSMTPError(t *testing.T) {
	c := newTestClient(t)
	c.sendMail = func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
		return errors.New("connection refused")
	}
	err := c.Send("user@example.com", "S", "B")
	if err == nil {
		t.Error("se esperaba error")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error no propaga causa: %v", err)
	}
}

func TestSendMessagePlainOnly(t *testing.T) {
	c := newTestClient(t)
	msg := string(c.buildRichMessage(Message{
		To:        "user@example.com",
		Subject:   "Hola",
		PlainBody: "Cuerpo plain",
	}))
	if !strings.Contains(msg, "Content-Type: text/plain; charset=utf-8\r\n") {
		t.Errorf("no es text/plain: %s", msg)
	}
	if strings.Contains(msg, "multipart/alternative") {
		t.Error("declara multipart aún sin HTMLBody")
	}
	if !strings.Contains(msg, "\r\n\r\nCuerpo plain") {
		t.Errorf("body plain ausente: %s", msg)
	}
}

func TestSendMessageMultipart(t *testing.T) {
	c := newTestClient(t)
	msg := string(c.buildRichMessage(Message{
		To:        "user@example.com",
		Subject:   "Resumen",
		PlainBody: "version plain",
		HTMLBody:  "<p>version html</p>",
	}))
	if !strings.Contains(msg, "multipart/alternative; boundary=\"mokuji_") {
		t.Errorf("no declara multipart con boundary: %s", msg)
	}
	if !strings.Contains(msg, "Content-Type: text/plain; charset=utf-8") {
		t.Error("falta parte plain")
	}
	if !strings.Contains(msg, "Content-Type: text/html; charset=utf-8") {
		t.Error("falta parte html")
	}
	if !strings.Contains(msg, "version plain") || !strings.Contains(msg, "<p>version html</p>") {
		t.Error("alguno de los bodies no fue incluido")
	}
}

func TestSendMessageExtraHeaders(t *testing.T) {
	c := newTestClient(t)
	msg := string(c.buildRichMessage(Message{
		To:        "user@example.com",
		Subject:   "S",
		PlainBody: "B",
		ExtraHeaders: map[string]string{
			"List-Unsubscribe":      "<https://mokuji.org/unsubscribe?token=xyz>",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}))
	if !strings.Contains(msg, "List-Unsubscribe: <https://mokuji.org/unsubscribe?token=xyz>\r\n") {
		t.Errorf("falta List-Unsubscribe: %s", msg)
	}
	if !strings.Contains(msg, "List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n") {
		t.Errorf("falta List-Unsubscribe-Post: %s", msg)
	}
}

func TestSendMessageIgnoresReservedHeaders(t *testing.T) {
	c := newTestClient(t)
	msg := string(c.buildRichMessage(Message{
		To:        "user@example.com",
		Subject:   "Original",
		PlainBody: "B",
		ExtraHeaders: map[string]string{
			"Subject":      "Spoofed",
			"From":         "evil@evil.com",
			"Content-Type": "text/evil",
		},
	}))
	if strings.Contains(msg, "Subject: Spoofed") {
		t.Error("Subject fue sobreescrito")
	}
	if strings.Contains(msg, "evil@evil.com") {
		t.Error("From fue sobreescrito")
	}
	if strings.Contains(msg, "text/evil") {
		t.Error("Content-Type fue sobreescrito")
	}
}

func TestSendMessageValidatesTo(t *testing.T) {
	c := newTestClient(t)
	c.sendMail = func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
		t.Fatal("sendMail no debería ser llamado")
		return nil
	}
	err := c.SendMessage(Message{To: "no-es-email", Subject: "S", PlainBody: "B"})
	if err == nil {
		t.Error("se esperaba error")
	}
}

func TestSendMessageRequiresPlain(t *testing.T) {
	c := newTestClient(t)
	c.sendMail = func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
		t.Fatal("sendMail no debería ser llamado")
		return nil
	}
	err := c.SendMessage(Message{To: "user@example.com", Subject: "S"})
	if err == nil {
		t.Error("se esperaba error por PlainBody vacío")
	}
}
