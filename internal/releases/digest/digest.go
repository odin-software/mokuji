// Package digest arma y manda el resumen semanal de releases por email
// (ADR 0013 §6).
//
// Responsabilidades:
//   - query: para un user con email_digest_enabled=1, encontrar caps
//     publicados desde su last_digest_sent_at (o desde "hace 7 días" la
//     primera vez), filtrados por idioma preferido + manga en su biblioteca
//     con status='reading'.
//   - render: dos plantillas (texto + HTML) embebidas en web/emails/.
//   - send: email multipart vía email.Client, con headers List-Unsubscribe
//     firmados con el signer de internal/unsubscribe.
//   - bookkeeping: tras un envío exitoso, escribir last_digest_sent_at.
//
// Un Sender es el componente compartido entre el worker semanal y el endpoint
// admin de prueba — ambos llaman a Sender.SendToUser(ctx, userID).
package digest

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/url"
	texttmpl "text/template"
	"time"

	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/unsubscribe"
)

// ErrNoNewChapters se devuelve por SendToUser cuando el user no tiene caps
// nuevos desde su último envío. El worker semanal lo trata como "skip
// silencioso"; el endpoint admin lo reporta al caller.
var ErrNoNewChapters = errors.New("sin capítulos nuevos")

// ErrDigestDisabled se devuelve cuando el user tiene email_digest_enabled=0.
// El worker filtra antes con un query; existe para defensa en el path admin.
var ErrDigestDisabled = errors.New("digest desactivado para este usuario")

// MaxItemsPerDigest es un tope defensivo sobre el número de caps por email.
// Si un user vuelve después de 6 meses con 200 caps acumulados, mandamos los
// 50 más recientes y confiamos en que la página /releases cubre el resto.
const MaxItemsPerDigest = 50

// firstRunLookback es la ventana inicial cuando last_digest_sent_at IS NULL.
// Evita que el primer envío incluya el histórico entero.
const firstRunLookback = 7 * 24 * time.Hour

// Config agrupa las dependencias del Sender.
type Config struct {
	DB      *sql.DB
	Email   *email.Client
	Signer  *unsubscribe.Signer
	Emails  fs.FS // contiene digest.txt y digest.html
	BaseURL string
	// FromAddress opcional: si está seteado, los digests se envían con este
	// From en vez del default del Client (auth@). Permite separar product
	// emails de auth emails — un user puede silenciar el digest sin perder
	// OTPs, y deliverability del digest no contamina la del auth.
	FromAddress string
	Logger      *slog.Logger
}

// Sender arma + envía digests. Construir con New.
type Sender struct {
	db          *sql.DB
	email       *email.Client
	signer      *unsubscribe.Signer
	baseURL     string
	fromAddress string
	logger      *slog.Logger
	textTmpl    *texttmpl.Template
	htmlTmpl    *template.Template
	now         func() time.Time // inyectable para tests
	// sendRetryBackoff es la espera antes del 1 reintento de SMTP.
	// Default: 5s. Tests pueden bajarlo a 0 para no demorar la suite.
	sendRetryBackoff time.Duration
}

// New construye un Sender. Parsea las dos plantillas eagerly para fallar al
// arranque si están rotas.
func New(cfg Config) (*Sender, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Email == nil {
		return nil, errors.New("Email requerido")
	}
	if cfg.Signer == nil {
		return nil, errors.New("Signer requerido")
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("BaseURL requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	textTmpl, err := texttmpl.ParseFS(cfg.Emails, "digest.txt")
	if err != nil {
		return nil, fmt.Errorf("parsear digest.txt: %w", err)
	}
	htmlTmpl, err := template.ParseFS(cfg.Emails, "digest.html")
	if err != nil {
		return nil, fmt.Errorf("parsear digest.html: %w", err)
	}

	return &Sender{
		db:               cfg.DB,
		email:            cfg.Email,
		signer:           cfg.Signer,
		baseURL:          cfg.BaseURL,
		fromAddress:      cfg.FromAddress,
		logger:           cfg.Logger,
		textTmpl:         textTmpl,
		htmlTmpl:         htmlTmpl,
		now:              time.Now,
		sendRetryBackoff: 5 * time.Second,
	}, nil
}

// Item es una fila del digest: un cap + metadata del manga, suficiente para
// renderizar línea + link. ReaderURL apunta a /r/{token} en nuestro server
// — un shortcut que valida el token HMAC, marca el cap leído, y redirige al
// lector externo en una sola navegación (ver internal/library/emailread.go).
type Item struct {
	MangaTitle    string
	ChapterNumber string
	ChapterTitle  string
	ReaderURL     string
	// chapterID es solo para uso interno: lo necesitamos para firmar el
	// token del shortcut. No se exporta para que las plantillas (que iteran
	// los Items) no lo serialicen accidentalmente.
	chapterID int64
}

// templateData es lo que pasamos a las plantillas. Exportado para tests.
type templateData struct {
	Items          []Item
	UnsubscribeURL string
	BaseURL        string
}

// SendToUser manda un digest a `userID` si tiene caps nuevos. Devuelve
// ErrNoNewChapters si no hay nada que mandar (el caller decide loguear o
// no), ErrDigestDisabled si el user opt-out, u otro error si la query/SMTP
// fallan.
//
// Tras un envío exitoso, actualiza users.last_digest_sent_at al `now()` del
// Sender — ése es el corte para el próximo digest.
func (s *Sender) SendToUser(ctx context.Context, userID int64) error {
	emailAddr, digestEnabled, lastSentAt, err := s.loadUser(ctx, userID)
	if err != nil {
		return err
	}
	if !digestEnabled {
		return ErrDigestDisabled
	}

	since := s.cutoff(lastSentAt)
	items, err := s.unreadChapters(ctx, userID, since, MaxItemsPerDigest)
	if err != nil {
		return fmt.Errorf("query digest: %w", err)
	}
	if len(items) == 0 {
		return ErrNoNewChapters
	}
	// Sobrescribimos ReaderURL para que apunte al shortcut /r/{token} en
	// vez del lector externo directo. El query trae external_url para que
	// loadUser-side fallback funcione, pero en el email nunca exponemos
	// la URL real — pasa por nuestro server primero (mark + redirect).
	for i := range items {
		items[i].ReaderURL = s.shortcutURL(userID, items[i].chapterID)
	}

	plainBody, htmlBody, err := s.render(userID, items)
	if err != nil {
		return fmt.Errorf("render digest: %w", err)
	}

	now := s.now().UTC()
	msg := email.Message{
		From:      s.fromAddress,
		To:        emailAddr,
		Subject:   s.subject(items),
		PlainBody: plainBody,
		HTMLBody:  htmlBody,
		ExtraHeaders: map[string]string{
			"List-Unsubscribe":      "<" + s.unsubscribeURL(userID) + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}
	if err := s.sendWithRetry(ctx, userID, msg); err != nil {
		return fmt.Errorf("enviar digest: %w", err)
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_digest_sent_at = ? WHERE id = ?`,
		now.Unix(), userID,
	); err != nil {
		// El email ya salió. Si falla la escritura, el próximo digest puede
		// duplicar — preferimos eso a no actualizar y duplicar siempre.
		s.logger.Error("persistir last_digest_sent_at", "user_id", userID, "err", err)
	}

	s.logger.Info("digest enviado", "user_id", userID, "items", len(items))
	return nil
}

// sendWithRetry envía el mensaje y, si falla, reintenta UNA vez tras
// sendRetryBackoff. Razón: SMTP transient (Resend hiccup, red caída
// momentánea) son la causa más común de failures aislados. Un retry
// barato evita perder digests de la semana entera por un timeout.
//
// Riesgo conocido: si el primer envío llegó pero el ACK se perdió post-DATA,
// el reintento produce un email duplicado. Es raro (>99% de timeouts son
// pre-DATA) y un duplicado es preferible a no llegar. Limitado a 1 retry
// para acotar el daño en el peor caso. El cap de digest_runs en el worker
// evita amplificar más allá de eso.
func (s *Sender) sendWithRetry(ctx context.Context, userID int64, msg email.Message) error {
	err := s.email.SendMessage(msg)
	if err == nil {
		return nil
	}
	s.logger.Warn("SMTP fallo, reintentando", "user_id", userID, "err", err, "backoff", s.sendRetryBackoff)
	if s.sendRetryBackoff > 0 {
		select {
		case <-time.After(s.sendRetryBackoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.email.SendMessage(msg)
}

func (s *Sender) loadUser(ctx context.Context, userID int64) (emailAddr string, digestEnabled bool, lastSentAt sql.NullInt64, err error) {
	var digest int
	err = s.db.QueryRowContext(ctx, `
		SELECT email, email_digest_enabled, last_digest_sent_at
		FROM users
		WHERE id = ? AND deleted_at IS NULL
	`, userID).Scan(&emailAddr, &digest, &lastSentAt)
	if err != nil {
		return "", false, sql.NullInt64{}, fmt.Errorf("cargar user %d: %w", userID, err)
	}
	return emailAddr, digest == 1, lastSentAt, nil
}

func (s *Sender) cutoff(lastSentAt sql.NullInt64) time.Time {
	if lastSentAt.Valid {
		return time.Unix(lastSentAt.Int64, 0).UTC()
	}
	return s.now().UTC().Add(-firstRunLookback)
}

func (s *Sender) unreadChapters(ctx context.Context, userID int64, since time.Time, limit int) ([]Item, error) {
	// Dedup por (manga, number, language): un cap del mismo número
	// publicado en N sources aparece UNA vez en el digest. Cursor por
	// número: marcar leído un row marca leídos sus hermanos de cualquier
	// scanlator.
	rows, err := s.db.QueryContext(ctx, `
		SELECT chapter.id, manga.title, chapter.number, chapter.title, chapter.source_id, chapter.external_url
		FROM chapter
		JOIN manga          ON manga.id = chapter.manga_id
		JOIN library_entries le ON le.manga_id = manga.id
		JOIN users          ON users.id = le.user_id
		WHERE le.user_id = ?
		  AND le.status = 'reading'
		  AND chapter.language = users.preferred_language
		  AND chapter.published_at > ?
		  AND (le.last_read_chapter_number IS NULL
		       OR CAST(chapter.number AS REAL) > le.last_read_chapter_number)
		  AND chapter.id = (
		    SELECT c2.id FROM chapter c2
		    WHERE c2.manga_id = chapter.manga_id
		      AND c2.number = chapter.number
		      AND c2.language = chapter.language
		    ORDER BY c2.published_at DESC, c2.id DESC
		    LIMIT 1
		  )
		ORDER BY chapter.published_at DESC
		LIMIT ?
	`, userID, since.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		var (
			it          Item
			number      sql.NullString
			title       sql.NullString
			sourceID    string
			externalURL sql.NullString
		)
		if err := rows.Scan(&it.chapterID, &it.MangaTitle, &number, &title, &sourceID, &externalURL); err != nil {
			return nil, err
		}
		it.ChapterNumber = number.String
		it.ChapterTitle = title.String
		// ReaderURL "raw" — el caller (SendToUser) lo sobrescribe con el
		// shortcut /r/{token} antes de renderear. Lo dejamos como fallback
		// para que tests del query puedan ver una URL razonable.
		if externalURL.Valid && externalURL.String != "" {
			it.ReaderURL = externalURL.String
		} else {
			it.ReaderURL = "https://mangadex.org/chapter/" + sourceID
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// shortcutURL produce el link del email que pasa por nuestro server:
// BaseURL + /r/{token}. Token = HMAC(userID, chapterID) con MaxAgeReadChapter
// (30 días). Ver internal/library/emailread.go para el handler.
func (s *Sender) shortcutURL(userID, chapterID int64) string {
	token := s.signer.SignReadChapter(userID, chapterID)
	return s.baseURL + "/r/" + token
}

func (s *Sender) render(userID int64, items []Item) (plain, html string, err error) {
	data := templateData{
		Items:          items,
		UnsubscribeURL: s.unsubscribeURL(userID),
		BaseURL:        s.baseURL,
	}

	var textBuf bytes.Buffer
	if err := s.textTmpl.ExecuteTemplate(&textBuf, "digest.txt", data); err != nil {
		return "", "", fmt.Errorf("plain: %w", err)
	}
	var htmlBuf bytes.Buffer
	if err := s.htmlTmpl.ExecuteTemplate(&htmlBuf, "digest.html", data); err != nil {
		return "", "", fmt.Errorf("html: %w", err)
	}
	return textBuf.String(), htmlBuf.String(), nil
}

func (s *Sender) unsubscribeURL(userID int64) string {
	token := s.signer.Sign(userID, unsubscribe.ScopeReleasesDigest)
	q := url.Values{"token": []string{token}}
	return s.baseURL + "/unsubscribe?" + q.Encode()
}

// subject genera el subject según cantidad de caps. Específico → mejor preview
// en clients que muestran preview text.
func (s *Sender) subject(items []Item) string {
	if len(items) == 1 {
		return "1 capítulo nuevo en tu biblioteca"
	}
	return fmt.Sprintf("%d capítulos nuevos en tu biblioteca", len(items))
}
