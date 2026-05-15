// Package email envía correos plain-text via SMTP (ADR 0007).
//
// El cliente habla SMTP estándar (stdlib net/smtp), sin SDK específico de
// provider. Los self-hosters configuran su SMTP via env vars; mokuji.org
// usa Resend. En desarrollo local apunta a Mailpit definido en compose.yaml.
package email

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// Config define la configuración SMTP del cliente.
type Config struct {
	Host     string // ej: "localhost", "smtp.resend.com"
	Port     int    // ej: 1025 (mailpit), 587 (Resend con STARTTLS)
	Username string // puede ser vacío (sin auth, ej. mailpit local)
	Password string
	From     string       // ej: `mokuji <auth@mokuji.org>`
	Logger   *slog.Logger // opcional; default slog.Default()
}

// Client envía emails via SMTP.
type Client struct {
	addr     string
	auth     smtp.Auth
	from     string       // string completo "Display Name <email>" o "email"
	fromAddr string       // solo la parte del email
	domain   string       // dominio del email From, para Message-ID
	logger   *slog.Logger
	sendMail sendMailFunc // inyectable para tests
}

// sendMailFunc tiene la firma de smtp.SendMail; permite inyectar un fake en
// tests sin levantar un servidor SMTP real.
type sendMailFunc func(addr string, a smtp.Auth, from string, to []string, msg []byte) error

// New construye un Client. Falla rápido si From no es un email válido.
func New(cfg Config) (*Client, error) {
	if cfg.Host == "" {
		return nil, errors.New("Host vacío")
	}
	if cfg.Port == 0 {
		return nil, errors.New("Port vacío")
	}
	if cfg.From == "" {
		return nil, errors.New("From vacío")
	}

	parsed, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("From inválido: %w", err)
	}
	at := strings.LastIndex(parsed.Address, "@")
	if at == -1 {
		return nil, fmt.Errorf("From sin @: %q", parsed.Address)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	return &Client{
		addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		auth:     auth,
		from:     cfg.From,
		fromAddr: parsed.Address,
		domain:   parsed.Address[at+1:],
		logger:   logger,
		sendMail: smtp.SendMail,
	}, nil
}

// Send envía un email plain-text al destinatario.
func (c *Client) Send(to, subject, body string) error {
	if _, err := mail.ParseAddress(to); err != nil {
		return fmt.Errorf("to inválido: %w", err)
	}
	msg := c.buildMessage(to, subject, body)
	if err := c.sendMail(c.addr, c.auth, c.fromAddr, []string{to}, msg); err != nil {
		return fmt.Errorf("smtp.SendMail: %w", err)
	}
	c.logger.Info("email enviado", "to", to, "subject", subject)
	return nil
}

// Message describe un email "rico" (multipart plain+HTML con headers extra),
// usado para product emails como el digest semanal de releases (ADR 0007,
// nota sobre product emails vs auth emails).
//
// PlainBody es obligatorio (fallback para clientes que no rendean HTML);
// HTMLBody es opcional — si está vacío, se manda como text/plain simple.
// ExtraHeaders se mergean al final; las claves duplicadas con headers fijos
// (From, To, Subject, Date, Message-ID, MIME-Version, Content-Type) se
// ignoran silenciosamente para no romper el formato MIME.
type Message struct {
	To           string
	Subject      string
	PlainBody    string
	HTMLBody     string
	ExtraHeaders map[string]string
}

// SendMessage envía un Message vía SMTP. Si HTMLBody es vacío, se manda como
// text/plain (equivalente a Send pero permitiendo ExtraHeaders).
func (c *Client) SendMessage(m Message) error {
	if _, err := mail.ParseAddress(m.To); err != nil {
		return fmt.Errorf("to inválido: %w", err)
	}
	if m.PlainBody == "" {
		return errors.New("PlainBody vacío")
	}
	msg := c.buildRichMessage(m)
	if err := c.sendMail(c.addr, c.auth, c.fromAddr, []string{m.To}, msg); err != nil {
		return fmt.Errorf("smtp.SendMail: %w", err)
	}
	c.logger.Info("email enviado", "to", m.To, "subject", m.Subject, "multipart", m.HTMLBody != "")
	return nil
}

// reservedHeaders son los headers que SendMessage controla directamente;
// ExtraHeaders no puede sobreescribirlos.
var reservedHeaders = map[string]struct{}{
	"from":                      {},
	"to":                        {},
	"subject":                   {},
	"date":                      {},
	"message-id":                {},
	"mime-version":              {},
	"content-type":              {},
	"content-transfer-encoding": {},
}

func (c *Client) buildMessage(to, subject, body string) []byte {
	return c.buildRichMessage(Message{To: to, Subject: subject, PlainBody: body})
}

func (c *Client) buildRichMessage(m Message) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", c.from)
	fmt.Fprintf(&buf, "To: %s\r\n", m.To)
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&buf, "Message-ID: %s\r\n", generateMessageID(c.domain))
	buf.WriteString("MIME-Version: 1.0\r\n")

	// Headers extra (List-Unsubscribe, List-Unsubscribe-Post, etc). Salteamos
	// las reservadas para que el caller no rompa la estructura MIME.
	for k, v := range m.ExtraHeaders {
		if _, reserved := reservedHeaders[strings.ToLower(k)]; reserved {
			continue
		}
		fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
	}

	if m.HTMLBody == "" {
		buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		buf.WriteString("Content-Transfer-Encoding: 8bit\r\n")
		buf.WriteString("\r\n")
		buf.WriteString(normalizeCRLF(m.PlainBody))
		return buf.Bytes()
	}

	// multipart/alternative: el cliente elige plain o HTML según
	// capacidad/preferencia. Plain primero (RFC 2046 dice "menos rich first").
	boundary := generateBoundary()
	fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=%q\r\n", boundary)
	buf.WriteString("\r\n")

	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	buf.WriteString(normalizeCRLF(m.PlainBody))
	buf.WriteString("\r\n")

	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	buf.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	buf.WriteString(normalizeCRLF(m.HTMLBody))
	buf.WriteString("\r\n")

	fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	return buf.Bytes()
}

// normalizeCRLF convierte cualquier line ending a CRLF (requerido por SMTP).
func normalizeCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func generateMessageID(domain string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("<%s@%s>", hex.EncodeToString(b), domain)
}

func generateBoundary() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "mokuji_" + hex.EncodeToString(b)
}
