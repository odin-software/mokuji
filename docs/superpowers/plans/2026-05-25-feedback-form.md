# Feedback Form Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agregar un endpoint `/feedback` auth-only que recolecta feedback de users vía form web y lo envía por email a un destino configurado por `FEEDBACK_EMAIL_TO`. Si la env var está vacía, el feature entero queda desactivado (handler no se monta, link de footer no aparece, página no existe).

**Architecture:** Paquete nuevo `internal/feedback/` con `Handler` y `Config`. Handler usa el `email.Client` existente (paquete `internal/email`) para enviar el email; rate-limita por `user_id` con el `ratelimit.Limiter` existente; usa `flash` existente para success state + redirect. Opt-in vía env var: si vacía, `main.go` no construye el handler y nada se monta. El link en el footer del layout se gatea con un template func `feedbackEnabled` que cualquier handler que renderice layout registra en su FuncMap.

**Tech Stack:** Go stdlib (`net/http`, `html/template`), paquetes internos existentes (`email`, `ratelimit`, `flash`, `session`, `csrf`). Sin nueva dependencia. Sin migrations (no toca DB).

**Branch:** `feat/feedback-form`. Tag al cerrar: `v1.0.1`.

---

## Decisiones cerradas con el user (2026-05-25)

- **Auth-only**: requiere session válida. Logout o anon → redirect a `/auth` igual que cualquier ruta gateada.
- **Opt-in vía env var**: `FEEDBACK_EMAIL_TO` define el destino del email. Vacío = feature desactivado (handler nil, link oculto, ruta 404). Permite a self-hosters no operar el canal.
- **Sin DB**: email directo, sin tabla. YAGNI hasta que volumen justifique un dashboard.
- **Anti-abuse**: rate limit por `user_id` (5 envíos/hora). Auth gating + el rate limit existente del OTP en `/auth/start` cubren el resto.
- **Sin captchas third-party**: rompe el "sin third-party" del producto.
- **Fields del form**: `tipo` (bug/feature/copy/otro) + `mensaje` (textarea). El email del user sale del session, no se pide.
- **`Reply-To`** del email es la dirección del user, para que el destinatario pueda responder directo.
- **Success state**: flash + redirect a `/` (volver a la app, no quedarte mirando una página de "gracias").
- **Numbering**: `v1.0.1` — un solo feature contenido.

---

## File Structure

| Archivo | Estado | Responsabilidad |
|---------|--------|-----------------|
| `internal/feedback/feedback.go` | nuevo | Handler + Config + IsEnabled. GET y POST del form. Render del template. Build del email. |
| `internal/feedback/feedback_test.go` | nuevo | Tests del handler (GET, POST, validación, rate limit, gate auth). |
| `web/templates/feedback.html` | nuevo | Form HTML server-rendered, hereda del layout. |
| `internal/server/server.go` | modificar | Agregar `Feedback *feedback.Handler` al `Config`. |
| `internal/server/routes.go` | modificar | Montar `Feedback` igual que los demás handlers opcionales. |
| `main.go` | modificar | Leer `FEEDBACK_EMAIL_TO`; si no vacío, construir `feedback.New(...)` y wirearlo en `server.Config`. También setear `feedback.SetEnabled(true)` para el template func. |
| `web/templates/layout.html` | modificar | Agregar `{{if feedbackEnabled}}<a href="/feedback">feedback</a>{{end}}` en ambos footers (sidebar + global). |
| `web/templates/settings.html` | modificar | Agregar link condicional a `/feedback` en la página de settings. |
| `internal/home/home.go` | modificar | Registrar `feedbackEnabled` en el FuncMap de los templates parseados. |
| `internal/library/library.go` | modificar | Idem. |
| `internal/profile/profile.go` | modificar | Idem. |
| `internal/releases/handler/handler.go` | modificar | Idem. |
| `internal/onboarding/onboarding.go` | modificar | Idem. |
| `internal/auth/auth.go` | modificar | Idem (landing/start/verify usan layout). |
| `internal/unsubscribe/unsubscribe.go` | modificar | Idem (página /unsubscribe usa layout). |
| `.env.example` | modificar | Documentar `FEEDBACK_EMAIL_TO`. |
| `docs/self-hosting.md` | modificar | Agregar `FEEDBACK_EMAIL_TO` a la tabla "Opcionales por feature". |
| `VERSION` | modificar | `v1.0.0` → `v1.0.1` (al final, antes de PAUSA). |

**Convenciones obligatorias (heredadas):**

- Copy 100% en español dominicano (tuteo). **Nunca** voseo: `lees` no `leés`, `prueba` no `probá`, `tú` no `vos`, `tienes` no `tenés`.
- Commits chicos, subject en español, lowercase.
- ADR sólo si la decisión tiene tradeoff real. Este plan NO genera ADRs.
- Confirmar con el user antes de push/merge a `main`. CD deploya al pushear.
- **Pausa explícita** antes del bump `v1.0.1` y antes del merge.

**Convenciones técnicas heredadas:**

- Tests son `go test` puro, sin frameworks.
- Templates son `html/template` con `{{define "title"}}` + `{{define "content"}}`.
- Handlers exponen `Mount(mux *http.ServeMux)` para registrar rutas.
- Email outbound vía `email.Client.SendMessage(email.Message{...})` (ver `internal/email/email.go:121-148`).
- Rate limit vía `ratelimit.New(rate.Limit, burst)` + `Allow(key)` (ver `internal/ratelimit/ratelimit.go:39-52`).
- Flash vía `flash.Flasher.Set(w, kind, msg)` (ver `internal/flash/flash.go:73-85`).
- Sessions vía `session.FromContext(r.Context())` (ver `internal/session/`).

---

## Task 1: Branch + scaffold del paquete feedback

**Files:**
- Create: `internal/feedback/feedback.go`
- Create: `internal/feedback/feedback_test.go`

- [ ] **Step 1: Crear branch desde main**

Run:
```bash
git checkout main
git pull origin main
git checkout -b feat/feedback-form
git status
```
Expected: `On branch feat/feedback-form`, working tree limpio, HEAD en `3866ecf` (v1.0.0).

- [ ] **Step 2: Test inicial — IsEnabled refleja el estado**

Create `internal/feedback/feedback_test.go`:

```go
package feedback

import (
	"testing"
)

func TestIsEnabledDefaultsFalse(t *testing.T) {
	// El package-level var arranca en false. Cualquier test que setee
	// debe restaurar (TestMain o t.Cleanup) — los tests futuros lo harán
	// vía t.Cleanup.
	if IsEnabled() {
		t.Errorf("IsEnabled() = true, want false al inicio del proceso")
	}
}

func TestSetEnabledTogglesFlag(t *testing.T) {
	t.Cleanup(func() { SetEnabled(false) })
	SetEnabled(true)
	if !IsEnabled() {
		t.Errorf("después de SetEnabled(true), IsEnabled() debe ser true")
	}
	SetEnabled(false)
	if IsEnabled() {
		t.Errorf("después de SetEnabled(false), IsEnabled() debe ser false")
	}
}
```

Run: `go test ./internal/feedback/ -run TestIsEnabled -v`
Expected: FAIL — `package feedback is not in std (...)` o `no Go files in ...`.

- [ ] **Step 3: Crear el paquete con IsEnabled/SetEnabled**

Create `internal/feedback/feedback.go`:

```go
// Package feedback implementa el formulario opt-in de feedback que envía
// al destino configurado por FEEDBACK_EMAIL_TO. Si la env var está vacía,
// main.go no construye el Handler y el paquete queda silencioso: IsEnabled
// reporta false y los handlers consumidores ocultan los CTAs.
package feedback

import "sync/atomic"

// enabled es el flag global del feature. Se accede atómicamente para que
// los reads concurrentes de templates renderizándose no se carreen contra
// el set inicial en main.go.
var enabled atomic.Bool

// IsEnabled reporta si el feature está activo. Las FuncMaps de los handlers
// que renderizan layout registran un "feedbackEnabled" que delega aquí.
func IsEnabled() bool {
	return enabled.Load()
}

// SetEnabled flipa el flag. Llamar una vez al startup desde main.go.
func SetEnabled(v bool) {
	enabled.Store(v)
}
```

Run: `go test ./internal/feedback/ -run TestIsEnabled -v`
Expected: 2 PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/feedback/feedback.go internal/feedback/feedback_test.go
git commit -m "$(cat <<'EOF'
feedback: scaffold del paquete con flag IsEnabled

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Handler + Config + GET /feedback (form)

**Files:**
- Modify: `internal/feedback/feedback.go`
- Modify: `internal/feedback/feedback_test.go`
- Create: `web/templates/feedback.html`

- [ ] **Step 1: Test del GET — renderiza el form para user autenticado**

Append to `internal/feedback/feedback_test.go`:

```go
import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/flash"
	"git.odin.do/kengru/mokuji/internal/ratelimit"
	"git.odin.do/kengru/mokuji/internal/session"
)

// templatesFS embebe los templates del repo para los tests. La ruta es
// relativa al paquete; subimos dos niveles a la raíz del repo y bajamos
// a web/templates.
//
//go:embed testdata/templates/*.html
var testTemplatesFS embed.FS

func testTemplates(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(testTemplatesFS, "testdata/templates")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	return sub
}

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	h, err := New(Config{
		Templates: testTemplates(t),
		// Email/Limiter/Flasher requeridos pero los tests del GET no los
		// ejercitan; pasamos zero-value y placeholders.
		Email:        &email.Client{},
		EmailTo:      "kg@odin.do",
		Limiter:      ratelimit.New(5, 5),
		Flasher:      flash.New(false),
	})
	if err != nil {
		t.Fatalf("feedback.New: %v", err)
	}
	return h
}

func withSession(req *http.Request, userID int64) *http.Request {
	s := &session.Session{UserID: userID, Email: "user@example.com"}
	return req.WithContext(session.NewContext(req.Context(), s))
}

func TestGetFeedbackRendersForm(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	h.Mount(mux)

	req := httptest.NewRequest(http.MethodGet, "/feedback", nil)
	req = withSession(req, 1)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, needle := range []string{
		`name="tipo"`,
		`name="mensaje"`,
		`bug`,
		`feature`,
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("form body missing %q", needle)
		}
	}
}
```

**Importante:** los tests del paquete `feedback` necesitan los templates parsed. Para mantener el paquete autónomo en tests, embebeemos copias de los templates necesarios bajo `internal/feedback/testdata/templates/`. Crear ese directorio en step 2.

- [ ] **Step 2: Copiar templates necesarios a testdata**

```bash
mkdir -p internal/feedback/testdata/templates
cp web/templates/layout.html internal/feedback/testdata/templates/
# feedback.html aún no existe; lo creamos en step 4
```

Verificar:
```bash
ls internal/feedback/testdata/templates/
```
Expected: `layout.html`.

- [ ] **Step 3: Implementación mínima del Handler + GET**

Append to `internal/feedback/feedback.go`:

```go
import (
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"git.odin.do/kengru/mokuji/internal/csrf"
	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/flash"
	"git.odin.do/kengru/mokuji/internal/ratelimit"
	"git.odin.do/kengru/mokuji/internal/session"
)

// Config agrupa las dependencias del Handler. Todos los campos son
// required cuando se construye con New.
type Config struct {
	Templates fs.FS // web/templates/ con layout.html + feedback.html
	Email     *email.Client
	// EmailTo es el destino donde caen los emails de feedback. Viene de
	// la env var FEEDBACK_EMAIL_TO.
	EmailTo string
	Limiter *ratelimit.Limiter
	Flasher *flash.Flasher
	Logger  *slog.Logger
}

// Handler sirve GET y POST /feedback. Sólo se construye cuando el
// feature está activo (env var seteada).
type Handler struct {
	tmpl    *template.Template
	email   *email.Client
	emailTo string
	limiter *ratelimit.Limiter
	flasher *flash.Flasher
	logger  *slog.Logger
}

// New construye un Handler. Falla si los templates no parsean o si los
// campos obligatorios están vacíos.
func New(cfg Config) (*Handler, error) {
	if cfg.Templates == nil {
		return nil, errors.New("Templates requerido")
	}
	if cfg.Email == nil {
		return nil, errors.New("Email requerido")
	}
	if cfg.EmailTo == "" {
		return nil, errors.New("EmailTo requerido")
	}
	if cfg.Limiter == nil {
		return nil, errors.New("Limiter requerido")
	}
	if cfg.Flasher == nil {
		return nil, errors.New("Flasher requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	funcs := template.FuncMap{
		"csrfInput": csrf.Input,
	}
	tmpl, err := template.New("base").Funcs(funcs).ParseFS(
		cfg.Templates, "layout.html", "feedback.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parsear templates feedback: %w", err)
	}

	return &Handler{
		tmpl:    tmpl,
		email:   cfg.Email,
		emailTo: cfg.EmailTo,
		limiter: cfg.Limiter,
		flasher: cfg.Flasher,
		logger:  cfg.Logger,
	}, nil
}

// Mount registra GET y POST /feedback en el mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /feedback", h.get)
	mux.HandleFunc("POST /feedback", h.post)
}

type formData struct {
	Session *session.Session
	Error   string // mensaje a renderizar arriba del form (validación)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		// El middleware de sesión debería haber redirigido antes de llegar
		// aquí; defensa en profundidad.
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "layout", formData{Session: s}); err != nil {
		h.logger.Error("renderizar feedback", "err", err)
	}
}

// post es stub por ahora; lo completamos en Task 3.
func (h *Handler) post(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
```

- [ ] **Step 4: Crear el template feedback.html**

Create `web/templates/feedback.html`:

```html
{{define "title"}}feedback · mokuji{{end}}
{{define "content"}}
<style>
.feedback { max-width: 36rem; margin: 0 auto; padding: 1.5rem 0 3rem; }
.feedback h1 { font-size: 1.75rem; letter-spacing: -0.02em; margin: 0 0 0.5rem; }
.feedback p.lead { color: var(--text-soft); margin: 0 0 1.5rem; line-height: 1.5; }
.feedback form { display: flex; flex-direction: column; gap: 1rem; }
.feedback label { font-size: 0.8125rem; font-weight: 600; color: var(--text-label); letter-spacing: 0.02em; }
.feedback select,
.feedback textarea {
  width: 100%; padding: 0.625rem 0.75rem;
  border: 1.5px solid var(--border);
  background: var(--input-bg);
  color: var(--text);
  font: inherit;
  border-radius: 0;
}
.feedback textarea { min-height: 9rem; resize: vertical; line-height: 1.45; }
.feedback select:focus, .feedback textarea:focus { outline: none; border-color: var(--accent); }
.feedback .help { font-size: 0.75rem; color: var(--text-muted); margin: -0.4rem 0 0; }
.feedback .error { background: var(--error-bg); border-left: 3px solid var(--error-border); color: var(--error-text); padding: 0.625rem 0.875rem; font-size: 0.875rem; }
.feedback button {
  align-self: flex-start;
  padding: 0.625rem 1.25rem;
  background: var(--button-bg); color: var(--button-fg);
  border: none;
  font: inherit; font-weight: 700;
  letter-spacing: 0.02em;
  cursor: pointer;
}
.feedback button:hover { background: var(--button-bg-hover); }
</style>
<main class="feedback">
  <h1>feedback</h1>
  <p class="lead">¿Encontraste un bug, quieres pedir una mejora, o algo no se entiende? Cuéntame.</p>

  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}

  <form method="POST" action="/feedback">
    {{csrfInput .Session}}

    <label for="tipo">tipo</label>
    <select id="tipo" name="tipo" required>
      <option value="">elige uno</option>
      <option value="bug">bug — algo no funciona</option>
      <option value="feature">feature — me gustaría que…</option>
      <option value="copy">copy — texto raro / typo</option>
      <option value="otro">otro</option>
    </select>

    <label for="mensaje">mensaje</label>
    <textarea id="mensaje" name="mensaje" required minlength="4" maxlength="4000" placeholder="¿Qué pasó? ¿Qué esperabas?"></textarea>
    <p class="help">Mínimo 4 caracteres, máximo 4000. Tu email queda visible para que pueda responderte.</p>

    <button type="submit">enviar</button>
  </form>
</main>
{{end}}
```

Y copiarlo a testdata para los tests:

```bash
cp web/templates/feedback.html internal/feedback/testdata/templates/
```

- [ ] **Step 5: Correr el test del GET — debe pasar**

Run: `go test ./internal/feedback/ -run TestGetFeedbackRendersForm -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/feedback/ web/templates/feedback.html
git commit -m "$(cat <<'EOF'
feedback: handler + template del GET /feedback

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: POST /feedback (validación + email + flash)

**Files:**
- Modify: `internal/feedback/feedback.go`
- Modify: `internal/feedback/feedback_test.go`

- [ ] **Step 1: Test del POST exitoso**

Append to `internal/feedback/feedback_test.go`:

```go
import (
	"net/smtp"
	"net/url"
	"strings"
)

// captureSender intercepta SendMail y guarda el último mensaje enviado.
type captureSender struct {
	to      []string
	from    string
	msg     []byte
	called  bool
	failErr error
}

func (c *captureSender) sendMail(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
	c.called = true
	c.to = to
	c.from = from
	c.msg = msg
	return c.failErr
}

func newEmailClient(t *testing.T, sender *captureSender) *email.Client {
	t.Helper()
	c, err := email.New(email.Config{
		Host:     "smtp.example.com",
		Port:     587,
		Username: "u",
		Password: "p",
		From:     "mokuji <auth@mokuji.local>",
	})
	if err != nil {
		t.Fatalf("email.New: %v", err)
	}
	c.SetSendMailForTesting(sender.sendMail)
	return c
}

func TestPostFeedbackSendsEmail(t *testing.T) {
	sender := &captureSender{}
	h, err := New(Config{
		Templates: testTemplates(t),
		Email:     newEmailClient(t, sender),
		EmailTo:   "kg@odin.do",
		Limiter:   ratelimit.New(5, 5),
		Flasher:   flash.New(false),
	})
	if err != nil {
		t.Fatalf("feedback.New: %v", err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)

	form := url.Values{}
	form.Set("tipo", "bug")
	form.Set("mensaje", "no me carga la biblioteca después del login")

	req := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withSession(req, 42)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d, want 303. body: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("location: got %q, want /", loc)
	}
	if !sender.called {
		t.Fatal("sender.called = false; expected email.SendMessage to have run")
	}
	if len(sender.to) != 1 || sender.to[0] != "kg@odin.do" {
		t.Errorf("to: got %v, want [kg@odin.do]", sender.to)
	}
	body := string(sender.msg)
	for _, needle := range []string{
		"Subject: [mokuji feedback] bug",
		"Reply-To: user@example.com",
		"no me carga la biblioteca",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("email body missing %q.\nfull body:\n%s", needle, body)
		}
	}
}
```

- [ ] **Step 2: Test de validación — POST con tipo vacío re-renderiza form con error**

Append to test file:

```go
func TestPostFeedbackValidatesType(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	h.Mount(mux)

	form := url.Values{}
	form.Set("tipo", "") // vacío
	form.Set("mensaje", "algo")

	req := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withSession(req, 1)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tipo") {
		t.Errorf("body missing 'tipo' in error message")
	}
}

func TestPostFeedbackValidatesMessage(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	h.Mount(mux)

	form := url.Values{}
	form.Set("tipo", "bug")
	form.Set("mensaje", "ab") // < 4 chars

	req := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withSession(req, 1)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}
```

Run: `go test ./internal/feedback/ -run TestPostFeedback -v`
Expected: FAIL — `post` actualmente devuelve 501.

- [ ] **Step 3: Implementar `post` con validación + email**

Replace the stub `post` method in `internal/feedback/feedback.go` with:

```go
const (
	minMessage = 4
	maxMessage = 4000
)

var validTipos = map[string]bool{
	"bug":     true,
	"feature": true,
	"copy":    true,
	"otro":    true,
}

func (h *Handler) post(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		h.renderError(w, s, "no pude leer el formulario, vuelve a intentarlo", http.StatusBadRequest)
		return
	}

	tipo := strings.TrimSpace(r.PostFormValue("tipo"))
	mensaje := strings.TrimSpace(r.PostFormValue("mensaje"))

	if !validTipos[tipo] {
		h.renderError(w, s, "elige un tipo válido (bug / feature / copy / otro)", http.StatusBadRequest)
		return
	}
	if len(mensaje) < minMessage {
		h.renderError(w, s, fmt.Sprintf("el mensaje es muy corto (mínimo %d caracteres)", minMessage), http.StatusBadRequest)
		return
	}
	if len(mensaje) > maxMessage {
		h.renderError(w, s, fmt.Sprintf("el mensaje es muy largo (máximo %d caracteres)", maxMessage), http.StatusBadRequest)
		return
	}

	if err := h.sendEmail(s, tipo, mensaje, r); err != nil {
		h.logger.Error("enviar feedback", "user_id", s.UserID, "err", err)
		h.renderError(w, s, "no pude enviar el email, intenta de nuevo en un minuto", http.StatusInternalServerError)
		return
	}

	h.flasher.Set(w, "success", "gracias, recibí tu feedback")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) renderError(w http.ResponseWriter, s *session.Session, msg string, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = h.tmpl.ExecuteTemplate(w, "layout", formData{Session: s, Error: msg})
}

func (h *Handler) sendEmail(s *session.Session, tipo, mensaje string, r *http.Request) error {
	ua := r.UserAgent()
	if ua == "" {
		ua = "(unknown)"
	}
	plain := fmt.Sprintf(`De: %s (user_id: %d)
Tipo: %s
User-Agent: %s

%s
`, s.Email, s.UserID, tipo, ua, mensaje)

	return h.email.SendMessage(email.Message{
		To:        h.emailTo,
		Subject:   "[mokuji feedback] " + tipo,
		PlainBody: plain,
		ExtraHeaders: map[string]string{
			"Reply-To": s.Email,
		},
	})
}
```

Importar `strings` arriba si no está ya.

- [ ] **Step 4: Correr tests del POST — deben pasar**

Run: `go test ./internal/feedback/ -run TestPostFeedback -v`
Expected: 3 PASS (Sends, ValidatesType, ValidatesMessage).

- [ ] **Step 5: Suite completa del paquete**

Run: `go test ./internal/feedback/ -v`
Expected: todos PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/feedback/feedback.go internal/feedback/feedback_test.go
git commit -m "$(cat <<'EOF'
feedback: POST con validación + envío de email

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Rate limit por user_id

**Files:**
- Modify: `internal/feedback/feedback.go`
- Modify: `internal/feedback/feedback_test.go`

- [ ] **Step 1: Test del rate limit**

Append to `internal/feedback/feedback_test.go`:

```go
import "golang.org/x/time/rate"

func TestPostFeedbackRateLimits(t *testing.T) {
	sender := &captureSender{}
	h, err := New(Config{
		Templates: testTemplates(t),
		Email:     newEmailClient(t, sender),
		EmailTo:   "kg@odin.do",
		// Burst 1, refill muy lento — la segunda request del mismo user
		// debe ser bloqueada.
		Limiter: ratelimit.New(rate.Every(time.Hour), 1),
		Flasher: flash.New(false),
	})
	if err != nil {
		t.Fatalf("feedback.New: %v", err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)

	doPost := func() *httptest.ResponseRecorder {
		form := url.Values{}
		form.Set("tipo", "bug")
		form.Set("mensaje", "primer mensaje del user")
		req := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = withSession(req, 7)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	first := doPost()
	if first.Code != http.StatusSeeOther {
		t.Fatalf("primer POST: got %d, want 303", first.Code)
	}

	second := doPost()
	if second.Code != http.StatusTooManyRequests {
		t.Errorf("segundo POST: got %d, want 429", second.Code)
	}
}
```

Importar `time` arriba.

- [ ] **Step 2: Correr el test — debe fallar (el handler aún no rate-limita)**

Run: `go test ./internal/feedback/ -run TestPostFeedbackRateLimits -v`
Expected: FAIL — el segundo POST devuelve 303, no 429.

- [ ] **Step 3: Agregar el rate limit al inicio del POST**

In `internal/feedback/feedback.go`, modify `post` to check the limiter **after** session resolution but **before** parsing the form:

```go
func (h *Handler) post(w http.ResponseWriter, r *http.Request) {
	s, ok := session.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}

	// Rate limit por user_id. La clave es deliberadamente prefijada para
	// no colisionar con buckets de otros endpoints (auth/start, etc.) si
	// más adelante un solo Limiter sirve a varios features.
	key := fmt.Sprintf("feedback:%d", s.UserID)
	if !h.limiter.Allow(key) {
		h.renderError(w, s, "demasiado feedback en poco tiempo. espera un rato y vuelve a intentar", http.StatusTooManyRequests)
		return
	}

	if err := r.ParseForm(); err != nil {
		// ... (resto igual)
```

- [ ] **Step 4: Correr el test — debe pasar**

Run: `go test ./internal/feedback/ -run TestPostFeedbackRateLimits -v`
Expected: PASS.

- [ ] **Step 5: Correr toda la suite del paquete**

Run: `go test ./internal/feedback/ -v`
Expected: todos PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/feedback/
git commit -m "$(cat <<'EOF'
feedback: rate limit por user_id en el POST

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Wire en server + main + FuncMaps de los handlers que renderizan layout

Esta task es la más amplia en archivos tocados pero cada touch es chico (1-3 líneas). El objetivo: cuando `FEEDBACK_EMAIL_TO` está set en main.go, (a) se construye el handler, (b) se monta en routes, (c) `feedback.SetEnabled(true)` se invoca, (d) todos los layouts pueden chequear `feedbackEnabled` para mostrar/ocultar el link.

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/routes.go`
- Modify: `main.go`
- Modify: `internal/home/home.go`
- Modify: `internal/library/library.go` (si parsea layout — verificar)
- Modify: `internal/profile/profile.go`
- Modify: `internal/releases/handler/handler.go`
- Modify: `internal/onboarding/onboarding.go`
- Modify: `internal/auth/auth.go`
- Modify: `internal/unsubscribe/unsubscribe.go`

### Step-by-step

- [ ] **Step 1: Agregar `Feedback *feedback.Handler` al server.Config**

In `internal/server/server.go`:

```go
import (
	// ... imports existentes
	"git.odin.do/kengru/mokuji/internal/feedback"
)
```

Agregar campo al `Config` struct (después de `Admin *admin.Handler`):

```go
	Feedback *feedback.Handler
```

- [ ] **Step 2: Montar Feedback en routes**

In `internal/server/routes.go`, después del bloque de `s.cfg.Admin` (alrededor de la línea 48), agregar:

```go
	if s.cfg.Feedback != nil {
		s.cfg.Feedback.Mount(mux)
	}
```

- [ ] **Step 3: Build smoke — debe compilar aunque main.go aún no setea Feedback**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 4: Wire en main.go**

In `main.go`, después del bloque que construye otros handlers opcionales (buscar el bloque de `unsubscribe.New` o `admin.New` como anclas), agregar:

```go
	var feedbackHandler *feedback.Handler
	if to := os.Getenv("FEEDBACK_EMAIL_TO"); to != "" {
		feedbackHandler, err = feedback.New(feedback.Config{
			Templates: htmlTemplatesFS, // mismo FS que los otros handlers
			Email:     emailClient,
			EmailTo:   to,
			Limiter:   ratelimit.New(rate.Every(12*time.Minute), 5), // 5 por hora
			Flasher:   flasher,
			Logger:    logger,
		})
		if err != nil {
			logger.Error("inicializar feedback handler", "err", err)
			os.Exit(1)
		}
		feedback.SetEnabled(true)
		logger.Info("feedback habilitado", "to", to)
	}
```

Y agregarlo al `server.Config{...}`:

```go
		Feedback: feedbackHandler,
```

Verificar antes de editar:
- El nombre real del FS embebido (probablemente `htmlTemplatesFS` o `templatesFS` — `grep "HTMLTemplates" main.go` para ver).
- El nombre del email client local (`emailClient`, `mailer`, etc.).
- El nombre del flasher (`flasher` probablemente).
- Que `rate` y `time` ya estén importados (probablemente sí).
- Que `feedback` esté en imports.

- [ ] **Step 5: Build smoke**

Run: `go build ./...`
Expected: clean. Si falla por nombres distintos a los asumidos, corregir.

- [ ] **Step 6: Agregar `feedbackEnabled` al FuncMap de internal/home**

In `internal/home/home.go`, modificar el `funcs` que se le pasa al template parser (alrededor de la línea 62):

```go
	funcs := template.FuncMap{
		"csrfInput":        csrfInput, // o el helper existente
		"appVersion":       func() string { return version.Value },
		"feedbackEnabled":  feedback.IsEnabled,
	}
```

Agregar `"git.odin.do/kengru/mokuji/internal/feedback"` a imports.

- [ ] **Step 7: Repetir para los demás paquetes**

Para cada uno de: `internal/library`, `internal/profile`, `internal/releases/handler`, `internal/onboarding`, `internal/auth`, `internal/unsubscribe`:

1. Abrir el archivo principal del paquete (el que llama `template.New(...).Funcs(...)`).
2. Si el archivo tiene un FuncMap explícito, agregar `"feedbackEnabled": feedback.IsEnabled`.
3. Si NO tiene FuncMap (raro pero posible), agregarlo:
   ```go
   funcs := template.FuncMap{"feedbackEnabled": feedback.IsEnabled}
   tmpl, err := template.New("base").Funcs(funcs).ParseFS(...)
   ```
4. Agregar el import del paquete `feedback`.

**Verification per package:**

```bash
grep -n "FuncMap\|template.New" internal/library/library.go internal/profile/profile.go internal/releases/handler/handler.go internal/onboarding/onboarding.go internal/auth/auth.go internal/unsubscribe/unsubscribe.go
```

Esto te muestra dónde está cada FuncMap. Si algún paquete NO tiene un template parsing (porque no renderiza layout) — saltarlo. El criterio: si el paquete renderiza una página que usa el layout (que contiene el footer), necesita `feedbackEnabled`.

- [ ] **Step 8: Build smoke después de cada batch**

Run: `go build ./...` después de cada 2-3 archivos. Más fácil aislar errores.

- [ ] **Step 9: Tests completos**

Run: `go test ./...`
Expected: PASS en todos los paquetes. Si algún test de algún paquete falla porque su parsing ahora requiere la func, leer el error y agregar `feedback.IsEnabled` también al FuncMap del test setup correspondiente.

- [ ] **Step 10: Commit**

```bash
git add internal/server/ main.go internal/home/ internal/library/ internal/profile/ internal/releases/ internal/onboarding/ internal/auth/ internal/unsubscribe/
git commit -m "$(cat <<'EOF'
feedback: wire en server + main + FuncMap feedbackEnabled en handlers

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Link en footer + settings

**Files:**
- Modify: `web/templates/layout.html`
- Modify: `web/templates/settings.html`

- [ ] **Step 1: Agregar link en el global footer**

In `web/templates/layout.html` línea 908 (verificar con grep), reemplazar:

```html
<footer class="app-footer">mokuji · <a href="/version">{{appVersion}}</a> · <a href="/privacy">privacidad</a> · 目次</footer>
```

por:

```html
<footer class="app-footer">mokuji · <a href="/version">{{appVersion}}</a> · <a href="/privacy">privacidad</a>{{if feedbackEnabled}} · <a href="/feedback">feedback</a>{{end}} · 目次</footer>
```

- [ ] **Step 2: Agregar link en el sidebar footer**

In `web/templates/layout.html` líneas 888-890, reemplazar:

```html
<div class="app-sidebar-version">
  <a href="/version">mokuji {{appVersion}}</a> · <a href="/privacy">privacidad</a> · 目次
</div>
```

por:

```html
<div class="app-sidebar-version">
  <a href="/version">mokuji {{appVersion}}</a> · <a href="/privacy">privacidad</a>{{if feedbackEnabled}} · <a href="/feedback">feedback</a>{{end}} · 目次
</div>
```

- [ ] **Step 3: Agregar link en settings.html**

Read `web/templates/settings.html` primero para entender la estructura. Buscar una sección coherente para el link (probablemente cerca del bottom, antes del danger zone de "eliminar cuenta").

Agregar un bloque tipo:

```html
{{if feedbackEnabled}}
<section class="settings-section">
  <h2>feedback</h2>
  <p>¿Algo no funciona o quieres pedir una mejora? <a href="/feedback">cuéntame</a>.</p>
</section>
{{end}}
```

Adaptar el class/HTML al patrón existente en settings.html (verificar el archivo antes para usar el shape correcto).

- [ ] **Step 4: Copiar layout y settings actualizados al testdata del paquete feedback**

```bash
cp web/templates/layout.html internal/feedback/testdata/templates/
```

(settings.html no se rendea desde el paquete feedback, no necesita copia.)

- [ ] **Step 5: Build + tests**

Run: `go build ./... && go test ./...`
Expected: todo verde.

- [ ] **Step 6: Smoke local opcional**

Si tienes mailpit corriendo (`make compose-up`):

```bash
FEEDBACK_EMAIL_TO=kg@odin.do make build && ./bin/mokuji &
sleep 2
# Verificar que con env var seteada, el footer tiene el link:
curl -sS http://localhost:8080/ | grep -E "feedback" | head -2
kill %1
```

Saltable si no quieres levantar local.

- [ ] **Step 7: Commit**

```bash
git add web/templates/layout.html web/templates/settings.html internal/feedback/testdata/
git commit -m "$(cat <<'EOF'
feedback: link condicional en footer y settings

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Documentación (.env.example + self-hosting.md)

**Files:**
- Modify: `.env.example`
- Modify: `docs/self-hosting.md`

- [ ] **Step 1: Agregar FEEDBACK_EMAIL_TO en .env.example**

En `.env.example`, agregar al final una nueva sección (no en el bloque de sources, es un feature distinto):

```
# --- Feedback opt-in ---
#
# Si FEEDBACK_EMAIL_TO está vacío, el feature queda completamente apagado:
# la ruta /feedback responde 404 y el link en el footer no aparece. Setear
# a tu email (o alias) para activar el formulario in-app. Sólo users
# autenticados pueden enviar feedback; rate-limited a 5/hora por user_id.
FEEDBACK_EMAIL_TO=
```

- [ ] **Step 2: Agregar a la tabla "Opcionales por feature" de self-hosting.md**

En `docs/self-hosting.md`, sección "### Opcionales por feature", agregar una fila a la tabla:

```markdown
| `FEEDBACK_EMAIL_TO` | (vacío = feature desactivado) | Destino del form `/feedback` in-app. Sólo users autenticados; rate-limit 5/hora por user_id. Si vacío, la ruta y el link en el footer no existen. |
```

Insertar la fila entre `ADMIN_TOKEN` y `ASURA_ENABLED` (el orden alfabético colaboraría aquí pero el orden temático actual es: auth → admin → sources; pongamos `FEEDBACK_EMAIL_TO` entre admin y sources, donde calza temáticamente como "otro opt-in").

- [ ] **Step 3: Verificar voseo y commit**

Run:
```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|\bvos\b)' .env.example docs/self-hosting.md
```
Expected: matches solo en líneas pre-existentes (si las hay) — verificar tus adiciones no introducen ninguna.

```bash
git add .env.example docs/self-hosting.md
git commit -m "$(cat <<'EOF'
docs: documentar FEEDBACK_EMAIL_TO en .env.example y self-hosting

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Bump VERSION + smoke + PAUSA

**Files:**
- Modify: `VERSION`

**Último commit antes de la pausa.** No taggear, no pushear, no mergear hasta confirmación del user.

- [ ] **Step 1: Bump VERSION a v1.0.1**

Replace contents of `VERSION` with:
```
v1.0.1
```
(con newline al final, igual que el archivo original).

- [ ] **Step 2: Smoke build**

Run: `make build`
Expected: build OK.

Verificar versión inyectada:
```bash
strings ./bin/mokuji | grep -E "^v1\.0\.1$" | head -3
```
Expected: aparece `v1.0.1` al menos una vez.

- [ ] **Step 3: Suite completa**

Run: `go test ./...`
Expected: todos PASS.

- [ ] **Step 4: Voseo sweep final sobre archivos nuevos/tocados**

Run:
```bash
grep -nE '(debés|corrés|contactá|incluí|podés|deseás|seguís|leés|elegís|probá|agregá|volvé|esperá|fijate|abrí|pegá|tenés|actualizá|\bvos\b)' internal/feedback/feedback.go web/templates/feedback.html
```
Expected: empty.

- [ ] **Step 5: Commit**

```bash
git add VERSION
git commit -m "$(cat <<'EOF'
v1.0.1

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 6: PAUSA OBLIGATORIA**

**Detenerse aquí.** No taggear, no pushear, no mergear a `main`.

El user revisa la branch `feat/feedback-form` y decide:

1. Ajustes de copy en el form (texto del lead, labels, placeholders).
2. Ajustes en el shape del email body (formato, headers).
3. Aprobación del merge + tag.

Pasos manuales post-aprobación (no incluidos en este plan):

- `git tag -a v1.0.1 -m "v1.0.1 — feedback form in-app"`
- `git checkout main && git merge --ff-only feat/feedback-form`
- `git push origin main` (dispara CD a mokuji.org)
- `git push origin v1.0.1`
- En prod: setear `FEEDBACK_EMAIL_TO=kg@odin.do` en `/etc/mokuji/env` + restart del servicio. **El user lo hace manualmente — no automatizable desde este plan.**
- Borrar la branch local + remota.

---

## Self-Review

**Spec coverage:**

| Requerimiento | Task que lo cubre |
|---------------|--------------------|
| Auth-only (require session) | Task 2 (GET redirect) + Task 3 (POST redirect) |
| Opt-in vía `FEEDBACK_EMAIL_TO` | Task 1 (IsEnabled), Task 5 (main.go gate), Task 6 (footer condicional) |
| Sin DB | Task 3 (sólo email, ninguna migration) |
| Fields tipo + mensaje | Task 2 (template), Task 3 (validación) |
| `Reply-To` con email del user | Task 3 (sendEmail) |
| Rate limit por user_id | Task 4 |
| Sin captchas | (no se introducen; rate limit + auth gating cubren) |
| Success state = flash + redirect | Task 3 (Flasher.Set + 303 redirect a /) |
| Link en footer condicional | Task 5 (FuncMap) + Task 6 (template) |
| Link en settings condicional | Task 6 (settings.html) |
| Numbering v1.0.1 | Task 8 |
| Branch feat/feedback-form | Task 1 |

**Placeholder scan:** revisado — no quedan TBDs, todos los snippets de código están completos, las dependencias se nombran y se construyen.

**Type consistency:**
- `Config` struct campos coinciden entre Task 1 declaración y Task 2 uso.
- `formData` shape coincide en GET y POST renders (`Session *session.Session`, `Error string`).
- `feedback.IsEnabled` firma `func() bool` consistente entre paquete y FuncMap registration.
- `Handler` métodos: `Mount`, `get`, `post` — todos coherentes.

**Riesgos:**

| Riesgo | Mitigación |
|--------|-----------|
| `internal/feedback/testdata/templates/layout.html` queda desincronizada con la real | Task 6 hace la última copia. Cualquier cambio futuro en layout requiere re-sincronizar. Aceptable: low-frequency change. |
| Algún paquete con FuncMap que olvidamos en Task 5 → template parsing falla con "feedbackEnabled not defined" cuando ese paquete renderice layout con el footer | Step 9 (`go test ./...`) lo captura inmediatamente. |
| Atomic.Bool API requiere Go ≥1.19 | go.mod declara 1.25; safe. |
| Email send falla en post → 500. ¿Loggeo del cuerpo del feedback antes del send para no perderlo? | No por v1.0.1 — un fallo SMTP raro no justifica complejidad. Si esto se vuelve un problema real, agregar log estructurado del intento como follow-up. |

Plan listo.
