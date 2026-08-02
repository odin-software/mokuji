# v1.0.5 — Hardening anti-abuso de OTP + break-glass · Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Recuperar el acceso a mokuji.org sin depender de email, y cerrar la raíz del OTP bombing para que restaurar la key de Resend sea seguro.

**Architecture:** Tres capas independientes. (1) Un break-glass: `POST /admin/grant-session` emite un token de un solo uso que `GET /auth/grant` canjea por sesión, con la lógica de tokens en un paquete `internal/grant` compartido entre `admin` (emisor) y `auth` (canjeador). (2) Defensas en `/auth/start`: Turnstile antes del envío, el breaker global partido en buckets separados para emails conocidos vs. desconocidos, y una cold-list DB-backed. (3) Un reaper periódico que drena `otp_codes` y `admin_grants` vencidos.

**Tech Stack:** Go (stdlib + `golang.org/x/time/rate`), SQLite vía `modernc.org/sqlite` v1.50.1, `html/template`, Cloudflare Turnstile.

**Spec:** [`docs/superpowers/specs/2026-08-02-otp-abuse-hardening-design.md`](../specs/2026-08-02-otp-abuse-hardening-design.md)

## Global Constraints

- **Idioma**: todo comentario de código, mensaje de commit y copy de UI en español. Copy de UI en **tuteo dominicano**, nunca voseo. Los comentarios explican el *porqué*, no el *qué* — seguir la densidad de los paquetes existentes.
- **Nunca el plaintext en DB**: tokens y códigos se guardan como SHA-256 hex, igual que `otp_codes.code_hash` y `sessions.id_hash`.
- **Fail-fast en prod**: `envBool("SECURE_COOKIES", false)` es el discriminante prod/dev. Vars requeridas usan `log.Fatal` cuando está en `true`, y default de dev + `logger.Warn` cuando está en `false` — mismo patrón que `UNSUBSCRIBE_SECRET` en [`main.go:248-255`](../../../main.go).
- **Limiters nil-safe**: `ratelimit.Limiter` retorna `true` en `Allow` si el receiver es nil. Todo campo nuevo de limiter en un `Config` debe documentar `nil = sin límite` y funcionar en tests sin cablearlo.
- **Errores genéricos al usuario**: los rechazos de `/auth/start` muestran siempre el mismo texto. Nunca revelar cuál capa disparó, ni si un email tiene cuenta.
- **Tests**: `go test ./...`. El helper `setupDB(t)` en [`internal/auth/auth_test.go:26`](../../../internal/auth/auth_test.go) corre las migraciones reales desde `os.DirFS("../../migrations")` — cualquier migración nueva entra automáticamente en todos los tests.
- **Commits**: chicos, subject corto en español, sin scope inventado. Prefijos en uso en el repo: `auth:`, `admin:`, `docs:`, `fix:`.

**Nota de secuencia:** las Tasks 1–3 son deployables solas y resuelven el "no puedo entrar". Si hay que cortar el trabajo por la mitad, ese es el punto de corte.

---

### Task 1: Paquete `internal/grant` + migración `admin_grants`

Emisión y canje de tokens de un solo uso. Vive en su propio paquete porque lo consumen dos: `internal/admin` emite, `internal/auth` canjea. Ponerlo en cualquiera de los dos crearía una dependencia rara entre ellos.

**Files:**
- Create: `migrations/0013_admin_grants.sql`
- Create: `internal/grant/grant.go`
- Test: `internal/grant/grant_test.go`

**Interfaces:**
- Consumes: nada.
- Produces:
  - `grant.TTL` (`time.Duration`, 5 min)
  - `grant.ErrInvalid` (`error`)
  - `grant.Issue(ctx context.Context, db *sql.DB, userID int64) (token string, err error)`
  - `grant.Consume(ctx context.Context, db *sql.DB, token string) (userID int64, err error)`
  - `grant.CleanupExpired(ctx context.Context, db *sql.DB) error`

- [ ] **Step 1: Escribir la migración**

Crear `migrations/0013_admin_grants.sql`:

```sql
-- 0013_admin_grants.sql
--
-- Tokens de un solo uso para recuperación de acceso administrativa
-- (break-glass): los emite POST /admin/grant-session y los canjea
-- GET /auth/grant por una sesión real.
--
-- Decisiones aplicadas:
--   token_hash  -> SHA-256 hex del token; nunca el plaintext en DB, igual
--                  que otp_codes.code_hash y sessions.id_hash
--   user_id FK  -> a diferencia de otp_codes, acá sí hay FK: el endpoint
--                  emisor rechaza users inexistentes, así que nunca hay
--                  grant huérfano. CASCADE limpia si se borra la cuenta.
--   used_at     -> marca consumo single-use; NULL si aún no se canjeó

CREATE TABLE admin_grants (
    token_hash TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
);

CREATE INDEX idx_admin_grants_expires ON admin_grants(expires_at);
```

- [ ] **Step 2: Escribir los tests que fallan**

Crear `internal/grant/grant_test.go`:

```go
package grant

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"git.odin.do/kengru/mokuji/internal/migrations"
)

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := migrations.Run(db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("aplicar migrations: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO users (id, email, created_at) VALUES (1, 'alice@example.com', ?)`,
		time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("sembrar user: %v", err)
	}
	return db
}

func TestIssueYConsumeRoundTrip(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	token, err := Issue(ctx, db, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(token) != 64 {
		t.Errorf("esperaba token de 64 hex chars (32 bytes), got %d", len(token))
	}

	userID, err := Consume(ctx, db, token)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if userID != 1 {
		t.Errorf("esperaba user_id 1, got %d", userID)
	}
}

func TestConsumeEsSingleUse(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	token, err := Issue(ctx, db, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := Consume(ctx, db, token); err != nil {
		t.Fatalf("primer Consume: %v", err)
	}

	if _, err := Consume(ctx, db, token); !errors.Is(err, ErrInvalid) {
		t.Errorf("token ya usado fue aceptado de nuevo (err=%v)", err)
	}
}

func TestConsumeRechazaVencido(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	token, err := Issue(ctx, db, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Envejecer el grant a mano en vez de esperar 5 minutos reales.
	past := time.Now().UTC().Add(-time.Minute).Unix()
	if _, err := db.Exec(`UPDATE admin_grants SET expires_at = ?`, past); err != nil {
		t.Fatalf("envejecer grant: %v", err)
	}

	if _, err := Consume(ctx, db, token); !errors.Is(err, ErrInvalid) {
		t.Errorf("token vencido fue aceptado (err=%v)", err)
	}
}

func TestConsumeRechazaTokenInexistente(t *testing.T) {
	db := setupDB(t)

	if _, err := Consume(context.Background(), db, "no-existe"); !errors.Is(err, ErrInvalid) {
		t.Errorf("token inexistente no dio ErrInvalid (err=%v)", err)
	}
}

func TestNoGuardaElPlaintext(t *testing.T) {
	db := setupDB(t)

	token, err := Issue(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	var stored string
	if err := db.QueryRow(`SELECT token_hash FROM admin_grants`).Scan(&stored); err != nil {
		t.Fatalf("leer token_hash: %v", err)
	}
	if stored == token {
		t.Error("el token está en DB en plaintext")
	}
}

func TestCleanupExpiredBorraSoloVencidos(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	if _, err := Issue(ctx, db, 1); err != nil {
		t.Fatalf("Issue vigente: %v", err)
	}
	// Un segundo grant, envejecido.
	old, err := Issue(ctx, db, 1)
	if err != nil {
		t.Fatalf("Issue viejo: %v", err)
	}
	past := time.Now().UTC().Add(-time.Hour).Unix()
	if _, err := db.Exec(
		`UPDATE admin_grants SET expires_at = ? WHERE token_hash = ?`, past, hashToken(old),
	); err != nil {
		t.Fatalf("envejecer grant: %v", err)
	}

	if err := CleanupExpired(ctx, db); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM admin_grants`).Scan(&n); err != nil {
		t.Fatalf("contar: %v", err)
	}
	if n != 1 {
		t.Errorf("esperaba 1 grant vigente tras cleanup, quedaron %d", n)
	}
}
```

- [ ] **Step 3: Correr los tests y verificar que fallan**

Run: `go test ./internal/grant/ -v`
Expected: FAIL — el paquete no compila, `undefined: Issue`, `undefined: Consume`, `undefined: ErrInvalid`, `undefined: hashToken`, `undefined: CleanupExpired`.

- [ ] **Step 4: Escribir la implementación**

Crear `internal/grant/grant.go`:

```go
// Package grant emite y canjea tokens de un solo uso para recuperación de
// acceso administrativa (break-glass).
//
// Existe porque el flow de auth de mokuji depende enteramente de email: si
// el envío se cae — key revocada, provider suspendido, quota agotada — no
// hay forma de entrar. Un grant rompe esa dependencia sin abrir una segunda
// vía de auth permanente: lo emite POST /admin/grant-session detrás de
// ADMIN_TOKEN, y lo canjea GET /auth/grant una sola vez.
//
// Vive en su propio paquete porque lo consumen dos lados: internal/admin
// emite e internal/auth canjea. Ponerlo en cualquiera de los dos ataría un
// paquete al otro sin razón.
package grant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// TTL es la ventana de validez de un grant. Corta a propósito: el token
// viaja en query string y termina en los access logs de nginx, así que la
// exposición se acota con tiempo, no con secreto.
const TTL = 5 * time.Minute

// ErrInvalid cubre token inexistente, ya usado y vencido — los tres con el
// mismo error. Distinguirlos le daría a quien pruebe tokens una señal sobre
// cuáles existieron alguna vez.
var ErrInvalid = errors.New("grant inválido")

// Issue crea un grant para userID y retorna el token en plaintext. El
// plaintext solo existe en este return: en DB queda únicamente su SHA-256.
//
// No valida que userID exista — de eso se encarga el caller, que además
// necesita distinguir "user inexistente" para responder 404. La FK de la
// tabla es la red de seguridad.
func Issue(ctx context.Context, db *sql.DB, userID int64) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)

	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO admin_grants (token_hash, user_id, created_at, expires_at)
		VALUES (?, ?, ?, ?)
	`, hashToken(token), userID, now.Unix(), now.Add(TTL).Unix()); err != nil {
		return "", err
	}
	return token, nil
}

// Consume canjea el token y retorna el user_id asociado. Retorna ErrInvalid
// si no existe, ya se usó o venció.
//
// El UPDATE condicional con RETURNING hace validación y consumo en una sola
// operación atómica: dos requests con el mismo token no pueden ganar ambos,
// porque el segundo ya no matchea used_at IS NULL.
func Consume(ctx context.Context, db *sql.DB, token string) (int64, error) {
	now := time.Now().UTC().Unix()
	var userID int64
	err := db.QueryRowContext(ctx, `
		UPDATE admin_grants
		SET used_at = ?
		WHERE token_hash = ? AND expires_at > ? AND used_at IS NULL
		RETURNING user_id
	`, now, hashToken(token), now).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrInvalid
	}
	if err != nil {
		return 0, err
	}
	return userID, nil
}

// CleanupExpired borra los grants vencidos. Sin retención extra: una vez
// pasado expires_at no hay nada que consultar, ni siquiera para auditoría
// — el uso queda en los logs, no en la tabla.
func CleanupExpired(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM admin_grants WHERE expires_at < ?`,
		time.Now().UTC().Unix(),
	)
	return err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 5: Correr los tests y verificar que pasan**

Run: `go test ./internal/grant/ -v`
Expected: PASS — 6 tests.

- [ ] **Step 6: Verificar que la migración no rompió nada**

Run: `go test ./...`
Expected: PASS. Todos los `setupDB` corren `0013` ahora; si algo falla acá es la migración, no el paquete.

- [ ] **Step 7: Commit**

```bash
git add migrations/0013_admin_grants.sql internal/grant/
git commit -m "grant: tokens single-use para break-glass de acceso"
```

---

### Task 2: `POST /admin/grant-session`

El emisor. Va en `internal/admin`, que ya tiene el chequeo de `ADMIN_TOKEN` en tiempo constante y el helper `respondJSON`.

**Files:**
- Create: `internal/httpx/httpx.go`
- Test: `internal/httpx/httpx_test.go`
- Modify: `internal/auth/auth.go:476-490` (`clientIP` delega en `httpx`)
- Modify: `internal/admin/admin.go` (Config, Handler, Mount, handler nuevo)
- Modify: `main.go:423-431` (pasar `BaseURL` y `GrantLimiter`)
- Test: `internal/admin/admin_test.go`

**Interfaces:**
- Consumes: `grant.Issue` de la Task 1.
- Produces:
  - `httpx.ClientIP(r *http.Request, trustProxy bool) string` — extraído del método privado de `auth`, que pasa a delegar en él.
  - Endpoint `POST /admin/grant-session`, body `email=<addr>` (form o query, igual que `triggerDigest`), respuesta `{"url": "<BaseURL>/auth/grant?t=<token>", "expires_in": "5m0s"}`.
  - Campos nuevos en `admin.Config`: `BaseURL string`, `GrantLimiter *ratelimit.Limiter`, `TrustProxy bool`.

- [ ] **Step 1: Extraer `clientIP` a `internal/httpx`**

`auth.Handler.clientIP` ya resuelve identificar al cliente y `admin` necesita
exactamente lo mismo para su rate limiter. En vez de duplicar el bloque, se
extrae a una función pura que ambos consumen.

Crear `internal/httpx/httpx.go`:

```go
// Package httpx agrupa helpers de HTTP compartidos entre handlers.
package httpx

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP identifica al cliente de r, para rate limiting y logs.
//
// trustProxy=true hace que lea X-Forwarded-For y tome la primera entrada —
// el cliente original; el resto de la cadena son proxies intermedios. Solo
// es seguro detrás de un reverse proxy de confianza: cualquiera puede mandar
// ese header, así que leerlo sin un proxy que lo sobreescriba permite evadir
// cualquier rate limit rotando el valor.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.Index(xff, ","); i != -1 {
				xff = xff[:i]
			}
			return strings.TrimSpace(xff)
		}
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
```

Crear `internal/httpx/httpx_test.go`:

```go
package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIPSinProxyIgnoraXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := ClientIP(req, false); got != "203.0.113.9" {
		t.Errorf("con trustProxy=false esperaba 203.0.113.9, got %q", got)
	}
}

func TestClientIPConProxyLeeXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := ClientIP(req, true); got != "1.2.3.4" {
		t.Errorf("esperaba 1.2.3.4, got %q", got)
	}
}

func TestClientIPTomaElPrimeroDeLaCadena(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.2, 10.0.0.3")

	if got := ClientIP(req, true); got != "1.2.3.4" {
		t.Errorf("esperaba el primer IP de la cadena, got %q", got)
	}
}

func TestClientIPCaeARemoteAddrSinXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:54321"

	if got := ClientIP(req, true); got != "203.0.113.9" {
		t.Errorf("sin XFF esperaba RemoteAddr, got %q", got)
	}
}

func TestClientIPRemoteAddrSinPuerto(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9"

	if got := ClientIP(req, false); got != "203.0.113.9" {
		t.Errorf("RemoteAddr sin puerto debía pasar tal cual, got %q", got)
	}
}
```

Run: `go test ./internal/httpx/ -v`
Expected: PASS — 5 tests.

- [ ] **Step 2: Hacer que `auth` delegue en el helper**

En `internal/auth/auth.go:476-490`, sustituir el cuerpo de `clientIP`:

```go
// clientIP identifica al cliente para rate limiting y logs.
func (h *Handler) clientIP(r *http.Request) string {
	return httpx.ClientIP(r, h.trustProxy)
}
```

Añadir `"git.odin.do/kengru/mokuji/internal/httpx"` a los imports de `auth.go`, y
quitar `"net"` si no queda otro uso en el archivo.

Run: `go build ./... && go test ./internal/auth/ ./internal/httpx/`
Expected: PASS. El comportamiento no cambia — los tests existentes de auth lo confirman.

- [ ] **Step 3: Escribir los tests que fallan**

Añadir a `internal/admin/admin_test.go` (crear el archivo si no existe, con el mismo `setupDB` que usa `internal/grant`):

```go
func TestGrantSessionRechazaSinToken(t *testing.T) {
	h := newGrantTestHandler(t, setupDB(t))

	req := httptest.NewRequest(http.MethodPost, "/admin/grant-session?email=alice@example.com", nil)
	w := httptest.NewRecorder()
	h.grantSession(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("esperaba 401 sin Authorization, got %d", w.Code)
	}
}

func TestGrantSessionRechazaUserInexistente(t *testing.T) {
	h := newGrantTestHandler(t, setupDB(t))

	req := httptest.NewRequest(http.MethodPost, "/admin/grant-session?email=nadie@example.com", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	h.grantSession(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("esperaba 404 para user inexistente, got %d", w.Code)
	}
}

func TestGrantSessionNoCreaUsers(t *testing.T) {
	db := setupDB(t)
	h := newGrantTestHandler(t, db)

	req := httptest.NewRequest(http.MethodPost, "/admin/grant-session?email=nadie@example.com", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	h.grantSession(httptest.NewRecorder(), req)

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("contar users: %v", err)
	}
	if n != 1 {
		t.Errorf("el endpoint creó un user; esperaba 1 (el sembrado), got %d", n)
	}
}

func TestGrantSessionDevuelveURLCanjeable(t *testing.T) {
	db := setupDB(t)
	h := newGrantTestHandler(t, db)

	req := httptest.NewRequest(http.MethodPost, "/admin/grant-session?email=alice@example.com", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	h.grantSession(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperaba 200, got %d (body=%s)", w.Code, w.Body.String())
	}

	var resp struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parsear respuesta: %v", err)
	}
	if !strings.HasPrefix(resp.URL, "https://test.local/auth/grant?t=") {
		t.Fatalf("URL inesperada: %q", resp.URL)
	}

	parsed, err := url.Parse(resp.URL)
	if err != nil {
		t.Fatalf("parsear URL: %v", err)
	}
	userID, err := grant.Consume(context.Background(), db, parsed.Query().Get("t"))
	if err != nil {
		t.Fatalf("el token emitido no es canjeable: %v", err)
	}
	if userID != 1 {
		t.Errorf("esperaba user_id 1, got %d", userID)
	}
}

func TestGrantSessionRespetaRateLimit(t *testing.T) {
	h := newGrantTestHandler(t, setupDB(t))
	// burst 0 => nunca hay token => siempre denegado.
	h.grantLim = ratelimit.New(rate.Every(time.Hour), 0)

	req := httptest.NewRequest(http.MethodPost, "/admin/grant-session?email=alice@example.com", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	h.grantSession(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("esperaba 429 con el limiter agotado, got %d", w.Code)
	}
}
```

Y el helper, en el mismo archivo:

```go
func newGrantTestHandler(t *testing.T, db *sql.DB) *Handler {
	t.Helper()
	return &Handler{
		db:      db,
		token:   "test-token",
		baseURL: "https://test.local",
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}
```

- [ ] **Step 4: Correr los tests y verificar que fallan**

Run: `go test ./internal/admin/ -run TestGrantSession -v`
Expected: FAIL — `h.grantSession undefined`, `h.baseURL undefined`, `h.grantLim undefined`.

- [ ] **Step 5: Añadir los campos a Config y Handler**

En `internal/admin/admin.go`, dentro de `type Config struct`:

```go
	// BaseURL es el origen público (ej: https://mokuji.org). Se usa para
	// construir la URL de canje que devuelve /admin/grant-session.
	BaseURL string
	// GrantLimiter acota /admin/grant-session por IP. nil = sin límite.
	GrantLimiter *ratelimit.Limiter
	// TrustProxy=true hace que el limiter lea X-Forwarded-For para
	// identificar al cliente. Solo seguro detrás de un proxy de confianza.
	TrustProxy bool
```

Dentro de `type Handler struct`:

```go
	baseURL    string
	grantLim   *ratelimit.Limiter
	trustProxy bool
```

Y en `New`, dentro del `return &Handler{...}`:

```go
		baseURL:    cfg.BaseURL,
		grantLim:   cfg.GrantLimiter,
		trustProxy: cfg.TrustProxy,
```

Añadir los imports que faltan: `"net/url"`, `"git.odin.do/kengru/mokuji/internal/grant"`, `"git.odin.do/kengru/mokuji/internal/httpx"`, `"git.odin.do/kengru/mokuji/internal/ratelimit"`.

- [ ] **Step 6: Registrar la ruta**

En `Mount`, después de la línea de `trigger-digest`:

```go
	mux.HandleFunc("POST /admin/grant-session", h.grantSession)
```

- [ ] **Step 7: Escribir el handler**

Añadir a `internal/admin/admin.go`:

```go
// grantSession emite un token de un solo uso que canjea GET /auth/grant por
// una sesión. Es el break-glass para cuando el envío de email está caído y
// nadie —ni el operador— puede completar el flow de OTP.
//
// Es un bypass de auth completo, así que está apretado a propósito:
//   - solo emite para users que YA existen; un ADMIN_TOKEN filtrado permite
//     impersonar cuentas existentes, no fabricar cuentas nuevas
//   - rate limit por IP encima del bearer token
//   - WARN en cada emisión, con IP y email, para que quede rastro
func (h *Handler) grantSession(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "no autorizado", http.StatusUnauthorized)
		return
	}
	if !h.grantLim.Allow(h.clientIP(r)) {
		http.Error(w, "demasiados intentos", http.StatusTooManyRequests)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	emailAddr := strings.TrimSpace(r.FormValue("email"))
	if emailAddr == "" {
		http.Error(w, "email requerido", http.StatusBadRequest)
		return
	}

	var userID int64
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM users WHERE email = ?`, emailAddr,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "user no existe", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("admin grant-session: buscar user", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	token, err := grant.Issue(r.Context(), h.db, userID)
	if err != nil {
		h.logger.Error("admin grant-session: emitir grant", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.logger.Warn("grant de sesión emitido",
		"user_id", userID, "email", emailAddr, "ip", h.clientIP(r))

	respondJSON(w, http.StatusOK, map[string]any{
		"url":        h.baseURL + "/auth/grant?t=" + url.QueryEscape(token),
		"expires_in": grant.TTL.String(),
	})
}

// clientIP identifica al cliente para el rate limiter.
func (h *Handler) clientIP(r *http.Request) string {
	return httpx.ClientIP(r, h.trustProxy)
}
```

Los imports de `admin.go` quedan: `"net/url"`, `"git.odin.do/kengru/mokuji/internal/grant"`,
`"git.odin.do/kengru/mokuji/internal/httpx"`, `"git.odin.do/kengru/mokuji/internal/ratelimit"`.
No hace falta `"net"` — vive ahora en `httpx`.

- [ ] **Step 8: Correr los tests y verificar que pasan**

Run: `go test ./internal/admin/ -v`
Expected: PASS — 5 tests nuevos más los que ya existían.

- [ ] **Step 9: Cablear en main.go**

En `main.go`, junto a los otros limiters (cerca de la línea 154):

```go
	grantLim := ratelimit.New(rate.Every(time.Minute), 5)
	go grantLim.Cleanup(rootCtx, 30*time.Minute)
```

Y en el `admin.New(admin.Config{...})` de la línea 423, añadir:

```go
		BaseURL:      envOr("BASE_URL", "http://localhost:8080"),
		GrantLimiter: grantLim,
		TrustProxy:   envBool("TRUST_PROXY", false),
```

- [ ] **Step 10: Verificar que compila y pasa todo**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/httpx/ internal/auth/
git commit -m "httpx: extrae ClientIP compartido entre auth y admin"
git add internal/admin/ main.go
git commit -m "admin: POST /admin/grant-session emite grant de break-glass"
```

---

### Task 3: `GET /auth/grant`

El canjeador. Cierra el break-glass: a partir de acá se puede entrar sin email.

**Files:**
- Modify: `internal/auth/auth.go` (Mount + handler nuevo)
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: `grant.Consume` y `grant.ErrInvalid` de la Task 1; `sessions.Create(w, r, userID)` que ya existe.
- Produces: ruta `GET /auth/grant?t=<token>`. 303 a `/` si el canje sale bien; 303 a `/auth` si el token es inválido.

- [ ] **Step 1: Escribir los tests que fallan**

Añadir a `internal/auth/auth_test.go`:

```go
func TestGrantCanjeaTokenYCreaSesion(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 5), &sent)

	if _, err := db.Exec(
		`INSERT INTO users (id, email, created_at) VALUES (1, 'alice@example.com', ?)`,
		time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("sembrar user: %v", err)
	}
	token, err := grant.Issue(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/grant?t="+token, nil)
	w := httptest.NewRecorder()
	h.grant(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("esperaba 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("esperaba redirect a /, got %q", loc)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = 1`).Scan(&n); err != nil {
		t.Fatalf("contar sesiones: %v", err)
	}
	if n != 1 {
		t.Errorf("esperaba 1 sesión creada, got %d", n)
	}
}

func TestGrantRechazaTokenInvalido(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 5), &sent)

	req := httptest.NewRequest(http.MethodGet, "/auth/grant?t=basura", nil)
	w := httptest.NewRecorder()
	h.grant(w, req)

	if loc := w.Header().Get("Location"); loc != "/auth" {
		t.Errorf("esperaba redirect a /auth con token inválido, got %q", loc)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("contar sesiones: %v", err)
	}
	if n != 0 {
		t.Errorf("se creó sesión con token inválido (n=%d)", n)
	}
}

func TestGrantNoReusableTrasCanje(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 5), &sent)

	if _, err := db.Exec(
		`INSERT INTO users (id, email, created_at) VALUES (1, 'alice@example.com', ?)`,
		time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("sembrar user: %v", err)
	}
	token, err := grant.Issue(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	h.grant(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/auth/grant?t="+token, nil))

	w := httptest.NewRecorder()
	h.grant(w, httptest.NewRequest(http.MethodGet, "/auth/grant?t="+token, nil))
	if loc := w.Header().Get("Location"); loc != "/auth" {
		t.Errorf("el token se canjeó dos veces (redirect=%q)", loc)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("contar sesiones: %v", err)
	}
	if n != 1 {
		t.Errorf("esperaba exactamente 1 sesión tras dos canjes, got %d", n)
	}
}
```

Añadir a los imports del test: `"git.odin.do/kengru/mokuji/internal/grant"`.

- [ ] **Step 2: Correr los tests y verificar que fallan**

Run: `go test ./internal/auth/ -run TestGrant -v`
Expected: FAIL — `h.grant undefined`.

- [ ] **Step 3: Registrar la ruta**

En `internal/auth/auth.go`, dentro de `Mount`, después de `GET /auth/verify`:

```go
	mux.HandleFunc("GET /auth/grant", h.grant)
```

- [ ] **Step 4: Escribir el handler**

Añadir a `internal/auth/auth.go`:

```go
// grant canjea un token emitido por POST /admin/grant-session por una
// sesión real. Es la mitad pública del break-glass: sin esto, un envío de
// email caído deja el sitio inaccesible hasta para el operador.
//
// No lleva rate limit propio: el token es de 256 bits y single-use, así que
// adivinarlo no es un ataque viable, y el emisor ya está limitado.
func (h *Handler) grant(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	if token == "" {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}

	userID, err := grant.Consume(r.Context(), h.db, token)
	if errors.Is(err, grant.ErrInvalid) {
		// Inválido, vencido o ya usado: los tres al mismo lugar, sin decir
		// cuál fue. WARN igual, porque un canje fallido en producción es
		// señal de que alguien está probando tokens.
		h.logger.Warn("canje de grant rechazado", "ip", h.clientIP(r))
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}
	if err != nil {
		h.logger.Error("canjear grant", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	if err := h.sessions.Create(w, r, userID); err != nil {
		// El grant ya quedó marcado como usado — Consume lo consumió de
		// forma atómica. Se pierde, y hay que emitir otro. Preferimos eso
		// a dejar vivo un token cuyo canje ya arrancó.
		h.logger.Error("crear sesión desde grant", "user_id", userID, "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	h.logger.Warn("sesión creada vía grant de break-glass",
		"user_id", userID, "ip", h.clientIP(r))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
```

Añadir `"git.odin.do/kengru/mokuji/internal/grant"` a los imports de `auth.go`.

- [ ] **Step 5: Correr los tests y verificar que pasan**

Run: `go test ./internal/auth/ -v`
Expected: PASS.

- [ ] **Step 6: Verificar el flow completo a mano**

```bash
make dev
```

En otra terminal, con `ADMIN_TOKEN=dev-token` en el entorno del server:

```bash
curl -s -X POST -H "Authorization: Bearer dev-token" "http://localhost:8080/admin/grant-session?email=tu@email.local"
```

Expected: JSON con `url` apuntando a `/auth/grant?t=...`. Abrir esa URL en el browser y confirmar que aterriza logueado en `/`. Repetir la misma URL: debe redirigir a `/auth`.

- [ ] **Step 7: Commit**

```bash
git add internal/auth/
git commit -m "auth: GET /auth/grant canjea grant por sesión"
```

---

### Task 4: Reaper de `otp_codes` y `admin_grants`

Hueco preexistente: no hay ningún reaper de `otp_codes`. Solo se borra dentro de `HardDeleteUser`. La tabla crece desde el día uno; el bombing nada más la infló hasta que se notó.

**Files:**
- Modify: `internal/auth/auth.go` (función exportada nueva)
- Modify: `main.go` (goroutine)
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: `grant.CleanupExpired` de la Task 1.
- Produces: `auth.CleanupExpiredCodes(ctx context.Context, db *sql.DB) (int64, error)` — retorna cuántas filas borró, para loguearlo.

- [ ] **Step 1: Escribir los tests que fallan**

Añadir a `internal/auth/auth_test.go`:

```go
func TestCleanupExpiredCodesRespetaRetencion(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Tres filas: vigente, vencida hace 1h (dentro de la retención de 24h),
	// y vencida hace 48h (fuera).
	rows := []struct {
		email   string
		expires int64
	}{
		{"vigente@example.com", now.Add(10 * time.Minute).Unix()},
		{"reciente@example.com", now.Add(-time.Hour).Unix()},
		{"vieja@example.com", now.Add(-48 * time.Hour).Unix()},
	}
	for _, row := range rows {
		if _, err := db.Exec(`
			INSERT INTO otp_codes (email, code_hash, created_at, expires_at)
			VALUES (?, 'hash', ?, ?)
		`, row.email, now.Unix(), row.expires); err != nil {
			t.Fatalf("sembrar %s: %v", row.email, err)
		}
	}

	deleted, err := CleanupExpiredCodes(ctx, db)
	if err != nil {
		t.Fatalf("CleanupExpiredCodes: %v", err)
	}
	if deleted != 1 {
		t.Errorf("esperaba 1 fila borrada (solo la de 48h), got %d", deleted)
	}

	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM otp_codes`).Scan(&remaining); err != nil {
		t.Fatalf("contar: %v", err)
	}
	if remaining != 2 {
		t.Errorf("esperaba 2 filas restantes, got %d", remaining)
	}

	// La fila reciente tiene que sobrevivir: la cold-list la necesita.
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM otp_codes WHERE email = 'reciente@example.com'`,
	).Scan(&n); err != nil {
		t.Fatalf("contar reciente: %v", err)
	}
	if n != 1 {
		t.Error("el reaper borró una fila dentro de la ventana de 24h que usa la cold-list")
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/auth/ -run TestCleanupExpiredCodes -v`
Expected: FAIL — `undefined: CleanupExpiredCodes`.

- [ ] **Step 3: Escribir la implementación**

Añadir a `internal/auth/auth.go`, cerca de `storeCode`:

```go
// codeRetention es cuánto se conservan los códigos ya vencidos.
//
// No es arbitrario: la cold-list de start consulta los códigos no
// verificados de las últimas 24h para detectar direcciones quemadas.
// Borrar más agresivo la desarma en silencio.
const codeRetention = 24 * time.Hour

// CleanupExpiredCodes borra los códigos vencidos fuera de la ventana de
// retención y retorna cuántas filas eliminó.
//
// Existe porque hasta v1.0.5 nada drenaba otp_codes: la única limpieza
// vivía dentro de HardDeleteUser, atada al borrado de cuenta. La tabla
// crecía indefinidamente, y el email bombing del 2026-06-04 la infló lo
// suficiente para que el problema saliera a la luz.
func CleanupExpiredCodes(ctx context.Context, db *sql.DB) (int64, error) {
	cutoff := time.Now().UTC().Add(-codeRetention).Unix()
	res, err := db.ExecContext(ctx,
		`DELETE FROM otp_codes WHERE expires_at < ?`, cutoff,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/auth/ -run TestCleanupExpiredCodes -v`
Expected: PASS.

- [ ] **Step 5: Cablear la goroutine en main.go**

Junto a `go cleanupSessions(rootCtx, sessions, logger)` (línea ~453):

```go
	go cleanupAuthArtifacts(rootCtx, db, logger)
```

Y la función, junto a `cleanupSessions` al final del archivo:

```go
// cleanupAuthArtifacts drena periódicamente los códigos OTP vencidos y los
// grants de break-glass vencidos. Un solo tick para ambos: son dos DELETEs
// baratos sobre tablas chicas y no hay razón para dos goroutines.
func cleanupAuthArtifacts(ctx context.Context, db *sql.DB, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := auth.CleanupExpiredCodes(ctx, db); err != nil {
				logger.Error("cleanup otp_codes", "err", err)
			} else if n > 0 {
				logger.Info("otp_codes vencidos borrados", "filas", n)
			}
			if err := grant.CleanupExpired(ctx, db); err != nil {
				logger.Error("cleanup admin_grants", "err", err)
			}
		}
	}
}
```

Añadir `"git.odin.do/kengru/mokuji/internal/grant"` a los imports de `main.go`.

- [ ] **Step 6: Verificar build y suite completa**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/auth/ main.go
git commit -m "auth: reaper horario de otp_codes y admin_grants vencidos"
```

---

### Task 5: Buckets separados para emails conocidos y desconocidos

Reemplaza el bucket global único. El bucket único es por qué el atacante podía bloquear el login del operador: se comía el presupuesto de todos.

**Files:**
- Modify: `internal/auth/auth.go` (Config, Handler, `start`)
- Modify: `main.go:150-170`
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: nada nuevo.
- Produces: `auth.Config.StartKnownLimiter` y `auth.Config.StartUnknownLimiter` (`*ratelimit.Limiter`) sustituyen a `StartGlobalLimiter`, que **se elimina**. Método privado `h.userExists(ctx, email) (bool, error)`.

- [ ] **Step 1: Escribir los tests que fallan**

Añadir a `internal/auth/auth_test.go`. Nota: el helper `newStartHandler` cambia de firma en el Step 3, así que estos tests usan uno nuevo:

```go
func newBucketHandler(t *testing.T, db *sql.DB, known, unknown *ratelimit.Limiter, sent *int) *Handler {
	t.Helper()
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	ec, err := email.New(email.Config{
		Host: "localhost", Port: 1025, From: "mokuji <auth@mokuji.org>", Logger: discard,
	})
	if err != nil {
		t.Fatalf("email.New: %v", err)
	}
	ec.SetSendMailForTesting(func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		*sent++
		return nil
	})
	h, err := New(Config{
		DB:                  db,
		Sessions:            session.New(db, false, discard),
		Email:               ec,
		HTMLTemplates:       os.DirFS("../../web/templates"),
		EmailTemplates:      os.DirFS("../../web/emails"),
		Logger:              discard,
		StartKnownLimiter:   known,
		StartUnknownLimiter: unknown,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func TestStartUsuarioConocidoNoConsumeBucketDesconocidos(t *testing.T) {
	db := setupDB(t)
	if _, err := db.Exec(
		`INSERT INTO users (id, email, created_at) VALUES (1, 'alice@example.com', ?)`,
		time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("sembrar user: %v", err)
	}

	sent := 0
	known := ratelimit.New(rate.Every(time.Minute), 5)
	// Desconocidos agotado: si alice cayera en este bucket, no se enviaría.
	unknown := ratelimit.New(rate.Every(time.Hour), 0)
	h := newBucketHandler(t, db, known, unknown, &sent)

	w := postStart(h, "alice@example.com")

	if sent != 1 {
		t.Errorf("un user conocido debió enviarse pese al bucket de desconocidos agotado, sent=%d", sent)
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("esperaba 303, got %d", w.Code)
	}
}

func TestStartDesconocidoNoConsumeBucketConocidos(t *testing.T) {
	db := setupDB(t)
	sent := 0
	// Conocidos con presupuesto, desconocidos agotado.
	known := ratelimit.New(rate.Every(time.Minute), 5)
	unknown := ratelimit.New(rate.Every(time.Hour), 0)
	h := newBucketHandler(t, db, known, unknown, &sent)

	w := postStart(h, "random@example.com")

	if sent != 0 {
		t.Errorf("un email desconocido se envió con su bucket agotado, sent=%d", sent)
	}
	if w.Code == http.StatusSeeOther {
		t.Error("redirigió a verify; debía mostrar error sin enviar")
	}
}

func TestStartFloodDeDesconocidosNoBloqueaAlConocido(t *testing.T) {
	db := setupDB(t)
	if _, err := db.Exec(
		`INSERT INTO users (id, email, created_at) VALUES (1, 'alice@example.com', ?)`,
		time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("sembrar user: %v", err)
	}

	sent := 0
	known := ratelimit.New(rate.Every(time.Minute), 5)
	unknown := ratelimit.New(rate.Every(time.Hour), 2)
	h := newBucketHandler(t, db, known, unknown, &sent)

	// Vaciar el bucket de desconocidos con direcciones distintas, como haría
	// el atacante. Los limiters por-email no lo frenan porque cada dirección
	// estrena bucket propio.
	for _, addr := range []string{"a@x.com", "b@x.com", "c@x.com", "d@x.com"} {
		postStart(h, addr)
	}
	sentTrasFlood := sent

	w := postStart(h, "alice@example.com")

	if sent != sentTrasFlood+1 {
		t.Errorf("el flood de desconocidos bloqueó al user conocido (sent %d -> %d)", sentTrasFlood, sent)
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("esperaba 303 para el conocido, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Correr los tests y verificar que fallan**

Run: `go test ./internal/auth/ -run 'TestStart(Usuario|Desconocido|Flood)' -v`
Expected: FAIL — `unknown field StartKnownLimiter in struct literal`.

- [ ] **Step 3: Sustituir el campo en Config y Handler**

En `internal/auth/auth.go`, **borrar** el campo `StartGlobalLimiter` y su bloque de comentario, y poner en su lugar:

```go
	// StartKnownLimiter y StartUnknownLimiter parten en dos el presupuesto
	// de envío de OTP, según si el email ya tiene cuenta.
	//
	// v1.0.4 tenía un solo bucket global, y ese era el problema: el atacante
	// se lo comía con direcciones random y dejaba sin presupuesto a los
	// users reales — incluido el operador. Convertía un DoS de quota en un
	// DoS de acceso.
	//
	// Partido, un flood de desconocidos solo agota su propio bucket. El
	// login de la gente con cuenta queda intacto, y el correo hacia extraños
	// —que es lo que genera bounces y quema reputación— tiene un techo
	// propio y bajo. nil = sin límite.
	StartKnownLimiter   *ratelimit.Limiter
	StartUnknownLimiter *ratelimit.Limiter
```

En `type Handler struct`, sustituir `startGlobalLim` por:

```go
	startKnownLim   *ratelimit.Limiter
	startUnknownLim *ratelimit.Limiter
```

Y en el `return &Handler{...}` de `New`, sustituir la asignación de `startGlobalLim` por:

```go
		startKnownLim:   cfg.StartKnownLimiter,
		startUnknownLim: cfg.StartUnknownLimiter,
```

- [ ] **Step 4: Añadir el lookup y sustituir el gate en `start`**

Añadir el helper cerca de `findOrCreateUser`:

```go
// userExists dice si el email ya tiene cuenta. Decide contra cuál bucket de
// envío se cobra el OTP.
func (h *Handler) userExists(ctx context.Context, emailAddr string) (bool, error) {
	var one int
	err := h.db.QueryRowContext(ctx,
		`SELECT 1 FROM users WHERE email = ?`, emailAddr,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
```

En `start`, sustituir el bloque completo del circuit breaker global (el `if !h.startGlobalLim.Allow("global")` y su comentario) por:

```go
	// Presupuesto de envío partido por tipo de destinatario. Ver el
	// comentario de StartKnownLimiter en Config para el porqué.
	known, err := h.userExists(r.Context(), emailAddr)
	if err != nil {
		h.logger.Error("consultar existencia de user", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	bucket, label := h.startUnknownLim, "desconocido"
	if known {
		bucket, label = h.startKnownLim, "conocido"
	}
	if !bucket.Allow("global") {
		h.logger.Warn("presupuesto de OTP agotado: envío frenado",
			"bucket", label, "ip", ip)
		h.render(w, r, "auth/start.html", startData{
			Email: emailAddr,
			Error: "demasiados intentos en pocos minutos. espera un poco y vuelve a intentar.",
		})
		return
	}
```

- [ ] **Step 5: Actualizar los tests viejos del breaker global**

Los tests `TestStartGlobalLimiterStopsSendingWhenExhausted` y `TestStartSendsWhenGlobalLimiterHasBudget` ([`internal/auth/auth_test.go:283`](../../../internal/auth/auth_test.go)) y el helper `newStartHandler` referencian `StartGlobalLimiter`. Los tests nuevos de la Task 5 cubren lo mismo con más precisión, así que:

- Borrar los dos tests viejos.
- Cambiar `newStartHandler` para que su parámetro `globalLim` cablee `StartUnknownLimiter` en vez de `StartGlobalLimiter` — los tests de las Tasks 3 y 6 lo siguen usando con direcciones que no tienen cuenta.

```go
	h, err := New(Config{
		DB:                  db,
		Sessions:            session.New(db, false, discard),
		Email:               ec,
		HTMLTemplates:       os.DirFS("../../web/templates"),
		EmailTemplates:      os.DirFS("../../web/emails"),
		Logger:              discard,
		StartUnknownLimiter: globalLim,
	})
```

- [ ] **Step 6: Correr la suite de auth**

Run: `go test ./internal/auth/ -v`
Expected: PASS.

- [ ] **Step 7: Cablear en main.go**

Sustituir la línea `startGlobalLim := ...` y su `go startGlobalLim.Cleanup(...)`:

```go
	// Presupuesto de envío partido por destinatario. Suman ~96 correos/día
	// en el peor caso, debajo del free tier de Resend (100/día).
	//   conocidos:    ~3/hora sostenido, burst 10 -> techo ~72/día
	//   desconocidos: ~1/hora sostenido, burst 3  -> techo ~24/día
	// Lo que importa no es el total sino el aislamiento: un flood de
	// direcciones random no puede tocar el presupuesto de login de la gente
	// que ya tiene cuenta.
	startKnownLim := ratelimit.New(rate.Every(20*time.Minute), 10)
	startUnknownLim := ratelimit.New(rate.Every(60*time.Minute), 3)
	go startKnownLim.Cleanup(rootCtx, 2*time.Hour)
	go startUnknownLim.Cleanup(rootCtx, 2*time.Hour)
```

Y en `auth.New(auth.Config{...})`, sustituir `StartGlobalLimiter: startGlobalLim,` por:

```go
		StartKnownLimiter:   startKnownLim,
		StartUnknownLimiter: startUnknownLim,
```

También actualizar el bloque de comentario de las líneas ~140-150 que documenta los límites: la línea del breaker global describe el bucket único que ya no existe.

- [ ] **Step 8: Verificar build y suite completa**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/auth/ main.go
git commit -m "auth: parte el presupuesto de OTP en buckets known/unknown"
```

---

### Task 6: Cold-list de direcciones quemadas

Defensa DB-backed: sobrevive reinicios, a diferencia de los limiters in-memory que se resetean en cada deploy del CD.

**Files:**
- Modify: `internal/auth/auth.go` (`start` + helper)
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: los buckets de la Task 5.
- Produces: `h.unverifiedCount(ctx, email) (int, error)` y la constante `coldListThreshold = 2`.

- [ ] **Step 1: Escribir los tests que fallan**

Añadir a `internal/auth/auth_test.go`:

```go
func TestStartColdListBloqueaDireccionQuemada(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 20), &sent)
	now := time.Now().UTC()

	// Dos códigos recientes sin verificar: la firma del bombing.
	for range 2 {
		if _, err := db.Exec(`
			INSERT INTO otp_codes (email, code_hash, created_at, expires_at)
			VALUES ('quemada@example.com', 'hash', ?, ?)
		`, now.Unix(), now.Add(-time.Minute).Unix()); err != nil {
			t.Fatalf("sembrar código: %v", err)
		}
	}

	w := postStart(h, "quemada@example.com")

	if sent != 0 {
		t.Errorf("se envió a una dirección en cold-list, sent=%d", sent)
	}
	if w.Code == http.StatusSeeOther {
		t.Error("redirigió a verify; debía frenar sin enviar")
	}
}

func TestStartColdListPermiteUnReintento(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 20), &sent)
	now := time.Now().UTC()

	// Un solo código sin verificar: el caso del user legítimo cuyo primer
	// correo cayó en spam. El umbral es 2 justamente para no castigarlo.
	if _, err := db.Exec(`
		INSERT INTO otp_codes (email, code_hash, created_at, expires_at)
		VALUES ('legitima@example.com', 'hash', ?, ?)
	`, now.Unix(), now.Add(-time.Minute).Unix()); err != nil {
		t.Fatalf("sembrar código: %v", err)
	}

	postStart(h, "legitima@example.com")

	if sent != 1 {
		t.Errorf("un solo código previo debió permitir reintento, sent=%d", sent)
	}
}

func TestStartColdListIgnoraCodigosViejos(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 20), &sent)
	now := time.Now().UTC()

	// Dos códigos sin verificar pero de hace 48h: fuera de la ventana. El
	// rastro de un ataque viejo no puede dejar una dirección bloqueada.
	for range 2 {
		if _, err := db.Exec(`
			INSERT INTO otp_codes (email, code_hash, created_at, expires_at)
			VALUES ('vieja@example.com', 'hash', ?, ?)
		`, now.Add(-48*time.Hour).Unix(), now.Add(-48*time.Hour).Unix()); err != nil {
			t.Fatalf("sembrar código: %v", err)
		}
	}

	postStart(h, "vieja@example.com")

	if sent != 1 {
		t.Errorf("códigos de hace 48h no debían bloquear, sent=%d", sent)
	}
}

func TestStartColdListIgnoraCodigosVerificados(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newStartHandler(t, db, ratelimit.New(rate.Every(time.Minute), 20), &sent)
	now := time.Now().UTC()

	// Dos códigos recientes, ambos verificados: es un user activo, no una
	// víctima de bombing.
	for range 2 {
		if _, err := db.Exec(`
			INSERT INTO otp_codes (email, code_hash, created_at, expires_at, used_at)
			VALUES ('activa@example.com', 'hash', ?, ?, ?)
		`, now.Unix(), now.Add(-time.Minute).Unix(), now.Unix()); err != nil {
			t.Fatalf("sembrar código: %v", err)
		}
	}

	postStart(h, "activa@example.com")

	if sent != 1 {
		t.Errorf("códigos ya verificados no debían bloquear, sent=%d", sent)
	}
}
```

- [ ] **Step 2: Correr los tests y verificar que fallan**

Run: `go test ./internal/auth/ -run TestStartColdList -v`
Expected: FAIL — `TestStartColdListBloqueaDireccionQuemada` falla con `sent=1`; los otros tres pasan por accidente (aún no hay gate). Los cuatro tienen que pasar al final.

- [ ] **Step 3: Escribir el helper**

Añadir a `internal/auth/auth.go`, junto a `userExists`:

```go
// coldListThreshold es cuántos códigos sin verificar en la ventana de
// retención bastan para dejar de enviarle a una dirección.
//
// Es 2 y no 1 a propósito: al user legítimo cuyo primer correo cayó en spam
// hay que dejarlo reintentar. A partir del tercero, el patrón ya no es
// "no me llegó" sino "alguien está usando esta dirección como destino".
const coldListThreshold = 2

// unverifiedCount cuenta los códigos emitidos a emailAddr dentro de la
// ventana de retención que nunca se verificaron.
//
// Es la firma exacta del email bombing: al atacante le mandan códigos a
// direcciones que no controla, así que jamás se consumen. A diferencia de
// los limiters in-memory —que se resetean en cada systemd restart, o sea en
// cada deploy del CD— esta señal vive en DB y sobrevive reinicios.
func (h *Handler) unverifiedCount(ctx context.Context, emailAddr string) (int, error) {
	cutoff := time.Now().UTC().Add(-codeRetention).Unix()
	var n int
	err := h.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM otp_codes
		WHERE email = ? AND used_at IS NULL AND created_at >= ?
	`, emailAddr, cutoff).Scan(&n)
	return n, err
}
```

- [ ] **Step 4: Insertar el gate en `start`**

En `start`, justo **después** del bloque de buckets de la Task 5 y **antes** de `code.Generate()`:

```go
	// Cold-list: si a esta dirección ya se le mandaron códigos que nunca se
	// verificaron, dejamos de mandarle. Acota el daño a una dirección
	// quemada una vez, en vez de en bucle.
	burned, err := h.unverifiedCount(r.Context(), emailAddr)
	if err != nil {
		h.logger.Error("consultar cold-list", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	if burned >= coldListThreshold {
		h.logger.Warn("dirección en cold-list: envío frenado",
			"intentos_sin_verificar", burned, "ip", ip)
		h.render(w, r, "auth/start.html", startData{
			Email: emailAddr,
			Error: "demasiados intentos en pocos minutos. espera un poco y vuelve a intentar.",
		})
		return
	}
```

- [ ] **Step 5: Correr los tests y verificar que pasan**

Run: `go test ./internal/auth/ -v`
Expected: PASS — los cuatro tests de cold-list y todos los anteriores.

- [ ] **Step 6: Commit**

```bash
git add internal/auth/
git commit -m "auth: cold-list DB-backed de direcciones quemadas"
```

---

### Task 7: Paquete `internal/turnstile`

Verificación contra la API de Cloudflare, aislada del handler para poder testearla contra un servidor fake.

**Files:**
- Create: `internal/turnstile/turnstile.go`
- Test: `internal/turnstile/turnstile_test.go`

**Interfaces:**
- Consumes: nada.
- Produces:
  - `turnstile.Verifier` (struct)
  - `turnstile.New(secretKey string) *Verifier`
  - `(*Verifier).Verify(ctx context.Context, token, remoteIP string) (bool, error)` — `nil` receiver retorna `(true, nil)`, igual que `ratelimit.Limiter.Allow`
  - `(*Verifier).SetEndpointForTesting(url string)`

- [ ] **Step 1: Escribir los tests que fallan**

Crear `internal/turnstile/turnstile_test.go`:

```go
package turnstile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeSiteverify(t *testing.T, success bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsear form: %v", err)
		}
		if r.FormValue("secret") == "" {
			t.Error("la request no mandó secret")
		}
		if r.FormValue("response") == "" {
			t.Error("la request no mandó response")
		}
		w.Header().Set("Content-Type", "application/json")
		if success {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVerifyAceptaTokenValido(t *testing.T) {
	srv := fakeSiteverify(t, true)
	v := New("secreto")
	v.SetEndpointForTesting(srv.URL)

	ok, err := v.Verify(context.Background(), "token-del-widget", "1.2.3.4")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("token válido fue rechazado")
	}
}

func TestVerifyRechazaTokenInvalido(t *testing.T) {
	srv := fakeSiteverify(t, false)
	v := New("secreto")
	v.SetEndpointForTesting(srv.URL)

	ok, err := v.Verify(context.Background(), "basura", "1.2.3.4")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Error("token inválido fue aceptado")
	}
}

func TestVerifyRechazaTokenVacioSinLlamarAlAPI(t *testing.T) {
	llamado := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llamado = true
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	t.Cleanup(srv.Close)

	v := New("secreto")
	v.SetEndpointForTesting(srv.URL)

	ok, err := v.Verify(context.Background(), "", "1.2.3.4")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Error("token vacío fue aceptado")
	}
	if llamado {
		t.Error("se llamó al API con token vacío; debía cortocircuitar")
	}
}

func TestVerifierNilPermiteTodo(t *testing.T) {
	var v *Verifier

	ok, err := v.Verify(context.Background(), "", "")
	if err != nil {
		t.Fatalf("Verify sobre nil: %v", err)
	}
	if !ok {
		t.Error("un Verifier nil debe permitir todo (dev sin keys)")
	}
}

func TestVerifyPropagaErrorDeRed(t *testing.T) {
	v := New("secreto")
	// Puerto cerrado: la llamada tiene que fallar, no retornar false en
	// silencio. El caller decide qué hacer con un Cloudflare caído.
	v.SetEndpointForTesting("http://127.0.0.1:1")

	if _, err := v.Verify(context.Background(), "token", "1.2.3.4"); err == nil {
		t.Error("esperaba error de red, got nil")
	}
}
```

- [ ] **Step 2: Correr los tests y verificar que fallan**

Run: `go test ./internal/turnstile/ -v`
Expected: FAIL — el paquete no existe.

- [ ] **Step 3: Escribir la implementación**

Crear `internal/turnstile/turnstile.go`:

```go
// Package turnstile verifica tokens de Cloudflare Turnstile contra el
// endpoint siteverify.
//
// Se usa como reto previo al envío de OTP en /auth/start, después del email
// bombing del 2026-06-04. El circuit breaker de v1.0.4 acotaba el volumen
// pero no impedía que el atacante siguiera usando mokuji para mandarle
// correo a extraños; el reto es lo que sube el costo por intento.
//
// Aislado del handler para poder testearlo contra un servidor fake sin
// hablar con Cloudflare.
package turnstile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultEndpoint = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// Verifier valida tokens contra siteverify.
type Verifier struct {
	secret   string
	endpoint string
	client   *http.Client
}

// New construye un Verifier. El timeout es corto a propósito: esto está en
// el camino crítico de un login, y un Cloudflare lento no puede colgar la
// request del usuario.
func New(secretKey string) *Verifier {
	return &Verifier{
		secret:   secretKey,
		endpoint: defaultEndpoint,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// SetEndpointForTesting apunta el Verifier a otro host. Solo para tests.
func (v *Verifier) SetEndpointForTesting(endpoint string) {
	v.endpoint = endpoint
}

// Verify consulta si el token del widget es válido. remoteIP es opcional
// (Cloudflare la usa como señal extra); pasar "" la omite.
//
// Si v es nil, retorna true — mismo contrato que ratelimit.Limiter.Allow.
// Es lo que permite correr en dev sin keys y sin ramas condicionales en el
// caller.
//
// Un error de red se propaga en vez de convertirse en false: "Cloudflare no
// responde" y "el usuario es un bot" son cosas distintas, y quien llama
// tiene que poder distinguirlas.
func (v *Verifier) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	if v == nil {
		return true, nil
	}
	if token == "" {
		return false, nil
	}

	form := url.Values{}
	form.Set("secret", v.secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("llamar siteverify: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("siteverify respondió %d", resp.StatusCode)
	}

	var body struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, fmt.Errorf("decodificar siteverify: %w", err)
	}
	return body.Success, nil
}
```

- [ ] **Step 4: Correr los tests y verificar que pasan**

Run: `go test ./internal/turnstile/ -v`
Expected: PASS — 5 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/turnstile/
git commit -m "turnstile: verifier contra siteverify de Cloudflare"
```

---

### Task 8: Cablear Turnstile en `/auth/start`

**Files:**
- Modify: `internal/auth/auth.go` (Config, Handler, `startData`, `start`, `startForm`)
- Modify: `web/templates/auth/start.html`
- Modify: `main.go`
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: `turnstile.New`, `(*Verifier).Verify` de la Task 7.
- Produces: `auth.Config.Turnstile *turnstile.Verifier` y `auth.Config.TurnstileSiteKey string`. `startData` gana el campo `TurnstileSiteKey string`.

- [ ] **Step 1: Escribir los tests que fallan**

Añadir a `internal/auth/auth_test.go`:

```go
func newTurnstileHandler(t *testing.T, db *sql.DB, v *turnstile.Verifier, sent *int) *Handler {
	t.Helper()
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	ec, err := email.New(email.Config{
		Host: "localhost", Port: 1025, From: "mokuji <auth@mokuji.org>", Logger: discard,
	})
	if err != nil {
		t.Fatalf("email.New: %v", err)
	}
	ec.SetSendMailForTesting(func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		*sent++
		return nil
	})
	h, err := New(Config{
		DB:                  db,
		Sessions:            session.New(db, false, discard),
		Email:               ec,
		HTMLTemplates:       os.DirFS("../../web/templates"),
		EmailTemplates:      os.DirFS("../../web/emails"),
		Logger:              discard,
		StartUnknownLimiter: ratelimit.New(rate.Every(time.Minute), 20),
		Turnstile:           v,
		TurnstileSiteKey:    "site-key-de-prueba",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func turnstileFake(t *testing.T, success bool) *turnstile.Verifier {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if success {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	t.Cleanup(srv.Close)
	v := turnstile.New("secreto")
	v.SetEndpointForTesting(srv.URL)
	return v
}

func postStartConTurnstile(h *Handler, emailAddr, tsToken string) *httptest.ResponseRecorder {
	form := "email=" + emailAddr + "&cf-turnstile-response=" + tsToken
	req := httptest.NewRequest(http.MethodPost, "/auth/start", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.start(w, req)
	return w
}

func TestStartRechazaSinTurnstile(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newTurnstileHandler(t, db, turnstileFake(t, true), &sent)

	w := postStartConTurnstile(h, "alice@example.com", "")

	if sent != 0 {
		t.Errorf("se envió sin resolver el reto, sent=%d", sent)
	}
	if w.Code == http.StatusSeeOther {
		t.Error("redirigió a verify sin reto resuelto")
	}
}

func TestStartRechazaTurnstileInvalido(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newTurnstileHandler(t, db, turnstileFake(t, false), &sent)

	w := postStartConTurnstile(h, "alice@example.com", "token-falso")

	if sent != 0 {
		t.Errorf("se envió con reto inválido, sent=%d", sent)
	}
	if w.Code == http.StatusSeeOther {
		t.Error("redirigió a verify con reto inválido")
	}
}

func TestStartEnviaConTurnstileValido(t *testing.T) {
	db := setupDB(t)
	sent := 0
	h := newTurnstileHandler(t, db, turnstileFake(t, true), &sent)

	w := postStartConTurnstile(h, "alice@example.com", "token-bueno")

	if sent != 1 {
		t.Errorf("esperaba 1 envío con reto resuelto, sent=%d", sent)
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("esperaba 303, got %d", w.Code)
	}
}

func TestStartSinVerifierNoExigeReto(t *testing.T) {
	db := setupDB(t)
	sent := 0
	// Verifier nil = dev sin keys. El flow tiene que seguir andando.
	h := newTurnstileHandler(t, db, nil, &sent)

	w := postStartConTurnstile(h, "alice@example.com", "")

	if sent != 1 {
		t.Errorf("sin Verifier el envío debía proceder, sent=%d", sent)
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("esperaba 303, got %d", w.Code)
	}
}
```

Añadir `"git.odin.do/kengru/mokuji/internal/turnstile"` a los imports del test.

- [ ] **Step 2: Correr los tests y verificar que fallan**

Run: `go test ./internal/auth/ -run TestStart.*Turnstile -v`
Expected: FAIL — `unknown field Turnstile in struct literal`.

- [ ] **Step 3: Añadir los campos**

En `internal/auth/auth.go`, dentro de `type Config struct`:

```go
	// Turnstile verifica el reto previo al envío de OTP. nil = sin reto
	// (dev local sin keys).
	//
	// El reto aplica a TODO /auth/start, no solo a emails sin cuenta. Si el
	// widget apareciera únicamente para desconocidos, su sola presencia
	// diría si una dirección está registrada — un oráculo de enumeración
	// gratis. Un solo camino, sin oráculo.
	Turnstile *turnstile.Verifier
	// TurnstileSiteKey es la clave pública que se renderiza en el widget.
	// Vacía = no se renderiza nada.
	TurnstileSiteKey string
```

En `type Handler struct`:

```go
	turnstile        *turnstile.Verifier
	turnstileSiteKey string
```

En el `return &Handler{...}` de `New`:

```go
		turnstile:        cfg.Turnstile,
		turnstileSiteKey: cfg.TurnstileSiteKey,
```

En `type startData struct`, añadir:

```go
	// TurnstileSiteKey vacía hace que la plantilla omita el widget.
	TurnstileSiteKey string
```

Añadir `"git.odin.do/kengru/mokuji/internal/turnstile"` a los imports de `auth.go`.

- [ ] **Step 4: Insertar el gate en `start`**

En `start`, justo **después** del chequeo de `startEmailLim` y **antes** del bloque de buckets de la Task 5:

```go
	// Reto antes de gastar presupuesto de envío. Va después de los limiters
	// baratos (in-memory) y antes de todo lo que toque DB o red, para que un
	// flood se coma lo menos posible por request.
	tsOK, err := h.turnstile.Verify(r.Context(),
		r.PostFormValue("cf-turnstile-response"), ip)
	if err != nil {
		// Cloudflare caído o inalcanzable. Fail-closed: preferimos un login
		// roto y ruidoso a reabrir el agujero que causó el incidente.
		h.logger.Error("verificar turnstile", "err", err, "ip", ip)
		h.render(w, r, "auth/start.html", h.startDataFor(startData{
			Email: emailAddr,
			Error: "no pudimos verificar el reto. intenta de nuevo en un momento.",
		}))
		return
	}
	if !tsOK {
		h.logger.Warn("reto de turnstile fallido", "ip", ip)
		h.render(w, r, "auth/start.html", h.startDataFor(startData{
			Email: emailAddr,
			Error: "no pudimos verificar que eres humano. recarga la página e intenta de nuevo.",
		}))
		return
	}
```

- [ ] **Step 5: Poblar la site key en todos los renders de start.html**

Añadir el helper junto a `render`:

```go
// startDataFor completa los campos de startData que salen de la config y no
// del request. Centralizarlo evita que un render nuevo se olvide de la site
// key y renderice la página sin widget.
func (h *Handler) startDataFor(d startData) startData {
	d.TurnstileSiteKey = h.turnstileSiteKey
	return d
}
```

Envolver con `h.startDataFor(...)` **todos** los `h.render(w, r, "auth/start.html", startData{...})` de `start` y `startForm`. Son los del limiter por IP, el de email inválido, el del limiter por email, los dos de Turnstile, el de buckets, el de cold-list, y el de `startForm`.

- [ ] **Step 6: Añadir el widget a la plantilla**

En `web/templates/auth/start.html`, dentro del `<form>`, justo antes del `<button>`:

```html
    {{if .TurnstileSiteKey}}
    <div class="cf-turnstile" data-sitekey="{{.TurnstileSiteKey}}"></div>
    {{end}}
```

Y al final del bloque `{{define "content"}}`, antes del `{{end}}`:

```html
{{if .TurnstileSiteKey}}
<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>
{{end}}
```

- [ ] **Step 7: Abrir la CSP para Turnstile**

**Sin esto el login se rompe en producción.** La CSP actual en
[`internal/server/middleware.go:110`](../../../internal/server/middleware.go) es
`script-src 'self'` y no declara `frame-src`, así que hereda `default-src 'self'`.
Turnstile carga un script desde `challenges.cloudflare.com` y renderiza el widget
dentro de un iframe del mismo origen: los dos quedan bloqueados.

En `internal/server/middleware.go`, sustituir la constante:

```go
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' https://challenges.cloudflare.com; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' https: data:; " +
	"connect-src 'self'; " +
	"frame-src https://challenges.cloudflare.com; " +
	"frame-ancestors 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"object-src 'none'"
```

Y añadir al bloque de comentario de arriba, después de la nota de `script-src`:

```go
//   - challenges.cloudflare.com en script-src y frame-src: el widget de
//     Turnstile carga su script desde ahí y se renderiza en un iframe. Es la
//     única excepción de origen externo en scripts; entró con v1.0.5 como
//     costo del reto anti-bombing. style-src ya tenía 'unsafe-inline', que es
//     lo otro que el widget necesita.
```

El test de headers en [`internal/server/server_test.go:65`](../../../internal/server/server_test.go)
compara contra `"default-src 'self'"`. Correr `go test ./internal/server/ -v` y, si
compara por igualdad exacta en vez de por prefijo, actualizar el valor esperado.

- [ ] **Step 8: Correr los tests y verificar que pasan**

Run: `go test ./internal/auth/ -v`
Expected: PASS.

- [ ] **Step 9: Cablear en main.go con fail-fast**

Antes del `auth.New(...)`:

```go
	// Turnstile: requerido en prod, opcional en dev. Mismo patrón que
	// UNSUBSCRIBE_SECRET — SECURE_COOKIES es el discriminante.
	tsSiteKey := os.Getenv("TURNSTILE_SITE_KEY")
	tsSecret := os.Getenv("TURNSTILE_SECRET_KEY")
	var tsVerifier *turnstile.Verifier
	if tsSiteKey == "" || tsSecret == "" {
		if envBool("SECURE_COOKIES", false) {
			log.Fatal("TURNSTILE_SITE_KEY y TURNSTILE_SECRET_KEY requeridos en producción")
		}
		logger.Warn("Turnstile no configurado: /auth/start sin reto (solo dev)")
	} else {
		tsVerifier = turnstile.New(tsSecret)
	}
```

Y en `auth.New(auth.Config{...})`:

```go
		Turnstile:        tsVerifier,
		TurnstileSiteKey: tsSiteKey,
```

Añadir `"git.odin.do/kengru/mokuji/internal/turnstile"` a los imports de `main.go`.

- [ ] **Step 10: Verificar el fail-fast**

```bash
SECURE_COOKIES=true go run . 2>&1 | head -3
```

Expected: `TURNSTILE_SITE_KEY y TURNSTILE_SECRET_KEY requeridos en producción` y exit distinto de 0.

- [ ] **Step 11: Verificar que dev sigue andando sin keys**

```bash
make dev
```

Expected: arranca, loguea el WARN de Turnstile, y `/auth` renderiza sin widget. Un POST a `/auth/start` manda el correo a Mailpit.

- [ ] **Step 12: Verificar build y suite completa**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 13: Commit**

```bash
git add internal/auth/ internal/server/ web/templates/auth/start.html main.go
git commit -m "auth: Turnstile como reto previo al envío de OTP"
```

---

### Task 9: Documentación y bump de versión

**Files:**
- Modify: `VERSION`
- Modify: `docs/estado.md` (sección v1.0.5, corrección de deliverability, env vars)
- Modify: `internal/version/version.go` si la versión está hardcodeada ahí

- [ ] **Step 1: Bump a v1.0.5**

Escribir `v1.0.5` en `VERSION`, y nada más. `internal/version/version.go` tiene
`var Value = "dev"` y el Makefile lo sobreescribe en build con
`-ldflags="-X ...internal/version.Value=$(VERSION)"` leyendo el archivo `VERSION`.
No hay versión hardcodeada que tocar.

- [ ] **Step 2: Corregir la afirmación falsa sobre deliverability**

En `docs/estado.md`, la línea de `SMTP_FROM_PRODUCT` (~línea 90) dice que separa auth de product "para deliverability independiente". Sustituir por:

```
SMTP_FROM_PRODUCT=mokuji <releases@mokuji.org>   # opcional; vacío = usa SMTP_FROM. Separa el From de product (digest) del de auth (OTP). OJO: NO da deliverability independiente — mismo dominio, misma alineación SPF/DKIM, misma reputación. La separación real exige subdominios verificados aparte (auth.mokuji.org / mail.mokuji.org); pendiente.
```

- [ ] **Step 3: Documentar las env vars nuevas**

En la sección "Env vars de producción" de `docs/estado.md`, después del bloque de Fase 2:

```
# v1.0.5 — required en prod (SECURE_COOKIES=true hace fail-fast si faltan)
TURNSTILE_SITE_KEY=<site key de Cloudflare Turnstile>
TURNSTILE_SECRET_KEY=<secret key de Cloudflare Turnstile>
```

- [ ] **Step 4: Escribir la sección v1.0.5**

Añadir a `docs/estado.md` antes de "Backlog post-v1.0.3":

```markdown
### v1.0.5 — hardening de OTP + break-glass

Cierra lo que v1.0.4 dejó abierto. El circuit breaker acotaba el volumen a
~92 envíos/día, pero no la raíz: esos 92 seguían yendo a extraños (bounces,
reputación), y el bucket único dejaba que el atacante bloqueara el login de
todos, operador incluido.

- **Break-glass** (`internal/grant`, migración `0013_admin_grants`):
  `POST /admin/grant-session` emite un token single-use de 256 bits (TTL 5
  min, solo el SHA-256 en DB, 404 si el user no existe) y `GET /auth/grant`
  lo canjea por sesión. Rompe la dependencia total del flow de auth con el
  email. `WARN` en emisión y canje.
- **Turnstile** (`internal/turnstile`): reto obligatorio antes del envío, en
  **todo** `/auth/start` — no solo en emails desconocidos, porque la
  presencia del widget sería un oráculo de enumeración. Fail-closed si
  Cloudflare no responde. Requerido con `SECURE_COOKIES=true`.
- **Buckets partidos**: `StartGlobalLimiter` se reemplaza por
  `StartKnownLimiter` (~3/h, burst 10, techo ~72/día) y
  `StartUnknownLimiter` (~1/h, burst 3, techo ~24/día). Un flood de
  direcciones random ya no puede tocar el presupuesto de login de la gente
  con cuenta.
- **Cold-list**: 2+ códigos sin verificar en 24h y se deja de enviar a esa
  dirección. DB-backed, así que sobrevive los reinicios que resetean los
  limiters in-memory — o sea, cada deploy del CD.
- **Reaper de `otp_codes`** (hueco preexistente, no del ataque): nada
  drenaba la tabla salvo `HardDeleteUser`. Goroutine horaria con retención
  de 24h, que es la ventana que necesita la cold-list. Borra también
  `admin_grants` vencidos.
- **Pendiente operativo**: `limit_req` de nginx en `/auth/start`, y
  subdominios de sending separados.
```

- [ ] **Step 5: Verificar build y suite completa**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add VERSION docs/estado.md
git commit -m "docs: v1.0.5 — hardening de OTP, break-glass y correcciones"
```

---

## Runbook de deploy

**No es parte del código. Lo ejecuta el operador, en este orden.** El hazard:
el CD deploya solo al pushear a `main`, y el fail-fast va a exigir las keys de
Turnstile. Pushear antes de ponerlas deja prod sin arrancar.

- [ ] **1.** Sacar site key + secret de Cloudflare Turnstile para `mokuji.org`.
- [ ] **2.** Agregar `TURNSTILE_SITE_KEY` y `TURNSTILE_SECRET_KEY` a `/etc/mokuji/env`. Confirmar que `ADMIN_TOKEN` esté poblado. **Sin restart.** El binario v1.0.4 ignora las vars nuevas.
- [ ] **3.** Backup de la DB y conteos previos:

```bash
sudo cp /var/lib/mokuji/mokuji.db /var/lib/mokuji/mokuji.db.pre-v1.0.5
```

```bash
sudo sqlite3 /var/lib/mokuji/mokuji.db "SELECT 'users', COUNT(*) FROM users UNION ALL SELECT 'otp_total', COUNT(*) FROM otp_codes UNION ALL SELECT 'otp_unused', COUNT(*) FROM otp_codes WHERE used_at IS NULL UNION ALL SELECT 'otp_emails_distintos', COUNT(DISTINCT email) FROM otp_codes;"
```

- [ ] **4.** Push a `main`. Verificar el deploy:

```bash
curl -s https://mokuji.org/version && ssh mimir 'systemctl status mokuji --no-pager'
```

- [ ] **5.** Recuperar acceso con el break-glass, todavía sin correo funcionando:

```bash
curl -s -X POST -H "Authorization: Bearer $ADMIN_TOKEN" "https://mokuji.org/admin/grant-session?email=TU_EMAIL"
```

Abrir la URL devuelta en el browser dentro de los 5 minutos.

- [ ] **6.** Purgar el backlog del ataque:

```bash
sudo sqlite3 /var/lib/mokuji/mokuji.db "DELETE FROM otp_codes WHERE expires_at < strftime('%s','now') - 86400; VACUUM;"
```

- [ ] **7.** Revisar `users`. Cualquiera que no reconozcas es un signup real o una prueba vieja — el bot no pudo crear cuentas. Si igual quieres borrarlo, va por `HardDeleteUser`, nunca por `DELETE` crudo:

```bash
sudo sqlite3 /var/lib/mokuji/mokuji.db "SELECT id, email, datetime(created_at,'unixepoch') FROM users ORDER BY id;"
```

- [ ] **8.** Key nueva de Resend en `SMTP_PASSWORD`, luego `sudo systemctl restart mokuji`. Probar un login real de punta a punta.
- [ ] **9.** `limit_req` en nginx sobre `/auth/start`:

```nginx
limit_req_zone $binary_remote_addr zone=authstart:10m rate=6r/m;

location = /auth/start {
    limit_req zone=authstart burst=3 nodelay;
    limit_req_status 429;
    proxy_pass http://127.0.0.1:8080;
}
```

- [ ] **10.** Tag anotado `v1.0.5` y borrar la memoria `resend-key-pendiente`.

Rollback en cualquier punto: `mokuji-deploy` deja `mokuji.prev`.
