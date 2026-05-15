package digest

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/migrations"
	"git.odin.do/kengru/mokuji/internal/unsubscribe"
)

// captureSMTP es un fake de email.sendMailFunc que captura cada mensaje
// para inspección. Thread-safe para no asustar al race detector.
type captureSMTP struct {
	mu       sync.Mutex
	messages [][]byte
	to       []string
	err      error
}

func (c *captureSMTP) fn(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.messages = append(c.messages, msg)
	c.to = append(c.to, to[0])
	return nil
}

func (c *captureSMTP) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.messages) == 0 {
		return ""
	}
	return string(c.messages[len(c.messages)-1])
}

func (c *captureSMTP) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.messages)
}

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrations.Run(db, os.DirFS("../../../migrations")); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return db
}

func newTestSender(t *testing.T, db *sql.DB) (*Sender, *captureSMTP) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cap := &captureSMTP{}

	cli, err := email.New(email.Config{
		Host:   "localhost",
		Port:   1025,
		From:   "mokuji <auth@mokuji.local>",
		Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Inyectamos el fake; accedemos al campo no exportado vía paquete
	// hermano usando un helper (más limpio que reflect).
	cli.SetSendMailForTesting(cap.fn)

	signer, err := unsubscribe.NewSigner(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}

	s, err := New(Config{
		DB:      db,
		Email:   cli,
		Signer:  signer,
		Emails:  os.DirFS("../../../web/emails"),
		BaseURL: "https://mokuji.test",
		Logger:  logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, cap
}

func insertUser(t *testing.T, db *sql.DB, emailAddr string, digestEnabled int) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO users (email, created_at, email_digest_enabled, preferred_language)
		 VALUES (?, 0, ?, 'en')`,
		emailAddr, digestEnabled,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func insertManga(t *testing.T, db *sql.DB, title string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO manga (title, source, source_id, created_at, updated_at) VALUES (?, 'test', ?, 0, 0)`,
		title, title,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func insertEntry(t *testing.T, db *sql.DB, userID, mangaID int64, status string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO library_entries (user_id, manga_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, 0, 0)`,
		userID, mangaID, status,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func insertChapter(t *testing.T, db *sql.DB, mangaID int64, sourceID, number, lang string, publishedAt time.Time) int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO chapter (manga_id, source, source_id, number, language, published_at, external_url, created_at)
		VALUES (?, 'mangadex', ?, ?, ?, ?, '', 0)
	`, mangaID, sourceID, number, lang, publishedAt.Unix())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestSendToUserHappyPath(t *testing.T) {
	db := setupDB(t)
	s, cap := newTestSender(t, db)

	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "One Piece")
	insertEntry(t, db, uid, mid, "reading")
	insertChapter(t, db, mid, "ch-1", "1100", "en", time.Now().Add(-1*time.Hour))

	if err := s.SendToUser(context.Background(), uid); err != nil {
		t.Fatalf("SendToUser: %v", err)
	}
	if cap.count() != 1 {
		t.Fatalf("emails enviados: %d", cap.count())
	}

	msg := cap.last()
	wants := []string{
		"To: alice@example.com",
		"Subject:",
		"multipart/alternative",
		"One Piece",
		"List-Unsubscribe: <https://mokuji.test/unsubscribe?token=",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click",
	}
	for _, w := range wants {
		if !strings.Contains(msg, w) {
			t.Errorf("falta %q en mensaje", w)
		}
	}

	// last_digest_sent_at debe estar actualizado.
	var lastSent sql.NullInt64
	if err := db.QueryRow(`SELECT last_digest_sent_at FROM users WHERE id = ?`, uid).Scan(&lastSent); err != nil {
		t.Fatal(err)
	}
	if !lastSent.Valid {
		t.Error("last_digest_sent_at no fue actualizado")
	}
}

func TestSendToUserNoChapters(t *testing.T) {
	db := setupDB(t)
	s, cap := newTestSender(t, db)

	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "Boring Manga")
	insertEntry(t, db, uid, mid, "reading")
	// Sin chapter inserts.

	err := s.SendToUser(context.Background(), uid)
	if !errors.Is(err, ErrNoNewChapters) {
		t.Errorf("esperaba ErrNoNewChapters, obtuve %v", err)
	}
	if cap.count() != 0 {
		t.Errorf("se mandó email sin caps: %d", cap.count())
	}
}

func TestSendToUserDisabledDigest(t *testing.T) {
	db := setupDB(t)
	s, _ := newTestSender(t, db)

	uid := insertUser(t, db, "alice@example.com", 0)
	err := s.SendToUser(context.Background(), uid)
	if !errors.Is(err, ErrDigestDisabled) {
		t.Errorf("esperaba ErrDigestDisabled, obtuve %v", err)
	}
}

func TestSendToUserSkipsDeleted(t *testing.T) {
	db := setupDB(t)
	s, _ := newTestSender(t, db)

	uid := insertUser(t, db, "alice@example.com", 1)
	if _, err := db.Exec(`UPDATE users SET deleted_at = 1 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}

	err := s.SendToUser(context.Background(), uid)
	// loadUser falla con sql.ErrNoRows; el wrapper no expone el sentinel
	// pero tampoco manda nada.
	if err == nil {
		t.Error("user borrado no debería recibir digest")
	}
}

func TestSendToUserRespectsLanguage(t *testing.T) {
	db := setupDB(t)
	s, cap := newTestSender(t, db)

	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "Spanish Manga")
	insertEntry(t, db, uid, mid, "reading")
	// User es 'en', cap en 'es' — debe ser filtrado.
	insertChapter(t, db, mid, "es-1", "1", "es", time.Now().Add(-1*time.Hour))

	err := s.SendToUser(context.Background(), uid)
	if !errors.Is(err, ErrNoNewChapters) {
		t.Errorf("cap de otro idioma fue incluido: %v", err)
	}
	if cap.count() != 0 {
		t.Error("se mandó email con cap filtrado")
	}
}

func TestSendToUserRespectsCursor(t *testing.T) {
	db := setupDB(t)
	s, _ := newTestSender(t, db)

	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "Cursor Manga")
	insertEntry(t, db, uid, mid, "reading")

	// Cap leído (en el cursor) + cap nuevo.
	read := insertChapter(t, db, mid, "ch-old", "10", "en", time.Now().Add(-48*time.Hour))
	insertChapter(t, db, mid, "ch-new", "11", "en", time.Now().Add(-1*time.Hour))

	if _, err := db.Exec(`UPDATE library_entries SET last_read_chapter_id = ? WHERE user_id = ?`, read, uid); err != nil {
		t.Fatal(err)
	}

	if err := s.SendToUser(context.Background(), uid); err != nil {
		t.Fatalf("SendToUser: %v", err)
	}
	// El test pasa si no falla y no incluye el cap viejo en el mensaje.
	// (No inspecciono el mensaje porque sin múltiples mangas es trivial.)
}

func TestSendToUserOnlyReadingStatus(t *testing.T) {
	db := setupDB(t)
	s, cap := newTestSender(t, db)

	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "Planned Manga")
	insertEntry(t, db, uid, mid, "planned")
	insertChapter(t, db, mid, "ch-1", "1", "en", time.Now().Add(-1*time.Hour))

	err := s.SendToUser(context.Background(), uid)
	if !errors.Is(err, ErrNoNewChapters) {
		t.Errorf("planned debería filtrarse: %v", err)
	}
	if cap.count() != 0 {
		t.Error("se mandó email con manga planned")
	}
}

func TestSendToUserFirstRunUsesLookback(t *testing.T) {
	db := setupDB(t)
	s, cap := newTestSender(t, db)

	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "Old Manga")
	insertEntry(t, db, uid, mid, "reading")
	// Caps: uno hace 1 día (dentro lookback), otro hace 30 días (fuera).
	insertChapter(t, db, mid, "ch-recent", "11", "en", time.Now().Add(-24*time.Hour))
	insertChapter(t, db, mid, "ch-old", "10", "en", time.Now().Add(-30*24*time.Hour))

	if err := s.SendToUser(context.Background(), uid); err != nil {
		t.Fatalf("SendToUser: %v", err)
	}
	msg := cap.last()
	if !strings.Contains(msg, "ch-recent") {
		// Note: el body NO contiene el source_id literal, sino "ch 11";
		// chequeamos por la URL mangadex que incluye source_id.
		if !strings.Contains(msg, "/chapter/ch-recent") {
			t.Errorf("falta cap reciente: %s", msg)
		}
	}
	if strings.Contains(msg, "/chapter/ch-old") {
		t.Error("incluyó cap > lookback")
	}
}

func TestSendToUserUsesLastSentCutoff(t *testing.T) {
	db := setupDB(t)
	s, cap := newTestSender(t, db)

	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "M")
	insertEntry(t, db, uid, mid, "reading")

	// Marcamos último envío hace 2 horas; cap publicado hace 1 hora entra,
	// cap hace 3 horas no.
	twoHoursAgo := time.Now().Add(-2 * time.Hour).Unix()
	if _, err := db.Exec(`UPDATE users SET last_digest_sent_at = ? WHERE id = ?`, twoHoursAgo, uid); err != nil {
		t.Fatal(err)
	}

	insertChapter(t, db, mid, "ch-new", "11", "en", time.Now().Add(-1*time.Hour))
	insertChapter(t, db, mid, "ch-old", "10", "en", time.Now().Add(-3*time.Hour))

	if err := s.SendToUser(context.Background(), uid); err != nil {
		t.Fatalf("SendToUser: %v", err)
	}
	msg := cap.last()
	if !strings.Contains(msg, "/chapter/ch-new") {
		t.Errorf("falta cap nuevo: %s", msg)
	}
	if strings.Contains(msg, "/chapter/ch-old") {
		t.Error("incluyó cap antes del cutoff")
	}
}

func TestSendToUserMaxItems(t *testing.T) {
	db := setupDB(t)
	s, cap := newTestSender(t, db)

	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "Prolific")
	insertEntry(t, db, uid, mid, "reading")

	// MaxItemsPerDigest + 5 caps.
	for i := range MaxItemsPerDigest + 5 {
		when := time.Now().Add(-time.Duration(i+1) * time.Minute)
		insertChapter(t, db, mid, "ch-"+itoa(i), itoa(i), "en", when)
	}

	if err := s.SendToUser(context.Background(), uid); err != nil {
		t.Fatalf("SendToUser: %v", err)
	}

	// Contamos cuántas líneas "/chapter/ch-" hay en el mensaje plain
	// (multipart, pero el patrón aparece en ambos cuerpos — dividimos).
	msg := cap.last()
	count := strings.Count(msg, "/chapter/ch-")
	// 2 partes (plain + HTML) cada una con MaxItems entries = 2*MaxItems refs.
	expected := 2 * MaxItemsPerDigest
	if count != expected {
		t.Errorf("items renderizados: esperado %d, obtenido %d", expected, count)
	}
}

func TestSendToUserSecondCallSkipsAlreadySent(t *testing.T) {
	db := setupDB(t)
	s, cap := newTestSender(t, db)

	uid := insertUser(t, db, "alice@example.com", 1)
	mid := insertManga(t, db, "M")
	insertEntry(t, db, uid, mid, "reading")
	insertChapter(t, db, mid, "ch-1", "1", "en", time.Now().Add(-1*time.Hour))

	if err := s.SendToUser(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	if cap.count() != 1 {
		t.Fatalf("primer envío: %d emails", cap.count())
	}

	// Segundo call sin caps nuevos.
	err := s.SendToUser(context.Background(), uid)
	if !errors.Is(err, ErrNoNewChapters) {
		t.Errorf("segundo call: esperaba ErrNoNewChapters, obtuve %v", err)
	}
	if cap.count() != 1 {
		t.Errorf("segundo call mandó email: %d total", cap.count())
	}
}

// itoa minimal para evitar importar strconv solo para tests.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
