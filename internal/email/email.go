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

func (c *Client) buildMessage(to, subject, body string) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", c.from)
	fmt.Fprintf(&buf, "To: %s\r\n", to)
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&buf, "Message-ID: %s\r\n", generateMessageID(c.domain))
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	buf.WriteString("\r\n")
	// SMTP requiere \r\n; normalizamos para evitar problemas con bodies que
	// usen \n a secas.
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\n", "\r\n")
	buf.WriteString(normalized)
	return buf.Bytes()
}

func generateMessageID(domain string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("<%s@%s>", hex.EncodeToString(b), domain)
}
