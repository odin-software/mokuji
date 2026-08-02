// Package auth implementa el flow de autenticación con email OTP (ADR 0004).
//
// Endpoints:
//
//	GET  /auth         -> form de email
//	POST /auth/start   -> recibir email, generar código, enviar por email
//	GET  /auth/verify  -> form de código
//	POST /auth/verify  -> validar código, crear/buscar usuario, crear sesión
//	POST /auth/logout  -> destruir sesión
package auth

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	htmltmpl "html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	texttmpl "text/template"
	"time"

	"git.odin.do/kengru/mokuji/internal/code"
	"git.odin.do/kengru/mokuji/internal/csrf"
	"git.odin.do/kengru/mokuji/internal/email"
	"git.odin.do/kengru/mokuji/internal/errrender"
	"git.odin.do/kengru/mokuji/internal/feedback"
	"git.odin.do/kengru/mokuji/internal/grant"
	"git.odin.do/kengru/mokuji/internal/httpx"
	"git.odin.do/kengru/mokuji/internal/ratelimit"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/turnstile"
	"git.odin.do/kengru/mokuji/internal/version"
)

const (
	codeDuration = 10 * time.Minute

	// codeRetention es cuánto se conservan los códigos ya vencidos.
	//
	// No es arbitrario: la cold-list de start consulta los códigos no
	// verificados de las últimas 24h para detectar direcciones quemadas.
	// Borrar más agresivo la desarma en silencio.
	codeRetention = 24 * time.Hour
)

// Config agrupa las dependencias del handler.
type Config struct {
	DB             *sql.DB
	Sessions       *session.Manager
	Email          *email.Client
	HTMLTemplates  fs.FS // contiene layout.html y auth/*.html
	EmailTemplates fs.FS // contiene otp.txt
	BaseURL        string
	Logger         *slog.Logger
	// TrustProxy=true hace que clientIP lea X-Forwarded-For. Solo seguro si
	// mokuji corre detrás de un reverse proxy de confianza (ej: nginx).
	TrustProxy        bool
	StartIPLimiter    *ratelimit.Limiter // limita /auth/start por IP; nil = sin límite
	StartEmailLimiter *ratelimit.Limiter // limita /auth/start por email; nil = sin límite
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
	VerifyIPLimiter     *ratelimit.Limiter       // limita /auth/verify por IP; nil = sin límite
	Errs                *errrender.ErrorRenderer // nil en tests sin assets

	// Turnstile verifica el reto previo al envío de OTP. nil = sin reto
	// (dev local sin keys).
	//
	// El reto aplica a TODO /auth/start, no solo a emails sin cuenta. Si el
	// widget apareciera únicamente para desconocidos, su sola presencia
	// diría si una dirección está registrada — un oráculo de enumeración
	// gratis. Un solo camino evita ese oráculo obvio.
	//
	// Queda un oráculo acotado: los buckets known/unknown se agotan por
	// separado, así que cuando el de desconocidos está en cero pero el de
	// conocidos tiene presupuesto, una dirección sin cuenta cae en 200 con
	// error y una con cuenta sigue a 303. Es autolimitado —cada sondeo
	// positivo gasta presupuesto del bucket known y le manda un correo real
	// a esa cuenta— y cuesta resolver Turnstile por intento, así que no
	// amerita rediseño. Pero existe, y toca nombrarlo en vez de asumir que
	// no hay ninguno.
	Turnstile *turnstile.Verifier
	// TurnstileSiteKey es la clave pública que se renderiza en el widget.
	// Vacía = no se renderiza nada.
	TurnstileSiteKey string
}

// Handler agrupa endpoints de /auth/*.
type Handler struct {
	db       *sql.DB
	sessions *session.Manager
	email    *email.Client
	pages    map[string]*htmltmpl.Template
	emailT   *texttmpl.Template
	// emailHTMLT es el render HTML de otp.html para multipart/alternative.
	// nil si la plantilla no existe (deploys más viejos / tests sin assets);
	// en ese caso se manda solo texto.
	emailHTMLT       *htmltmpl.Template
	baseURL          string
	logger           *slog.Logger
	trustProxy       bool
	startIPLim       *ratelimit.Limiter
	startEmailLim    *ratelimit.Limiter
	startKnownLim    *ratelimit.Limiter
	startUnknownLim  *ratelimit.Limiter
	verifyIPLim      *ratelimit.Limiter
	errs             *errrender.ErrorRenderer
	turnstile        *turnstile.Verifier
	turnstileSiteKey string
}

// New construye un Handler. Falla si las plantillas no parsean.
func New(cfg Config) (*Handler, error) {
	if cfg.DB == nil {
		return nil, errors.New("DB requerido")
	}
	if cfg.Sessions == nil {
		return nil, errors.New("Sessions requerido")
	}
	if cfg.Email == nil {
		return nil, errors.New("Email requerido")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	funcs := htmltmpl.FuncMap{
		"csrfInput":       csrf.Input,
		"appVersion":      func() string { return version.Value },
		"feedbackEnabled": feedback.IsEnabled,
	}

	base, err := htmltmpl.New("base").Funcs(funcs).ParseFS(cfg.HTMLTemplates, "layout.html")
	if err != nil {
		return nil, fmt.Errorf("parsear layout: %w", err)
	}

	pages := map[string]*htmltmpl.Template{}
	for _, page := range []string{"auth/start.html", "auth/verify.html"} {
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("clonar base para %s: %w", page, err)
		}
		if _, err := clone.ParseFS(cfg.HTMLTemplates, page); err != nil {
			return nil, fmt.Errorf("parsear %s: %w", page, err)
		}
		pages[page] = clone
	}

	emailT, err := texttmpl.ParseFS(cfg.EmailTemplates, "otp.txt")
	if err != nil {
		return nil, fmt.Errorf("parsear otp.txt: %w", err)
	}

	// otp.html es opcional: si existe, vamos a mandar multipart con HTML +
	// texto. Si no existe (assets viejos / FS sintético en tests), seguimos
	// con solo texto. fs.Stat es el chequeo barato; un error genérico también
	// cae al fallback sin romper.
	var emailHTMLT *htmltmpl.Template
	if _, statErr := fs.Stat(cfg.EmailTemplates, "otp.html"); statErr == nil {
		emailHTMLT, err = htmltmpl.ParseFS(cfg.EmailTemplates, "otp.html")
		if err != nil {
			return nil, fmt.Errorf("parsear otp.html: %w", err)
		}
	}

	return &Handler{
		db:               cfg.DB,
		sessions:         cfg.Sessions,
		email:            cfg.Email,
		pages:            pages,
		emailT:           emailT,
		emailHTMLT:       emailHTMLT,
		baseURL:          cfg.BaseURL,
		logger:           cfg.Logger,
		trustProxy:       cfg.TrustProxy,
		startIPLim:       cfg.StartIPLimiter,
		startEmailLim:    cfg.StartEmailLimiter,
		startKnownLim:    cfg.StartKnownLimiter,
		startUnknownLim:  cfg.StartUnknownLimiter,
		verifyIPLim:      cfg.VerifyIPLimiter,
		errs:             cfg.Errs,
		turnstile:        cfg.Turnstile,
		turnstileSiteKey: cfg.TurnstileSiteKey,
	}, nil
}

// Mount registra los endpoints en el mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth", h.startForm)
	mux.HandleFunc("POST /auth/start", h.start)
	mux.HandleFunc("GET /auth/verify", h.verifyForm)
	mux.HandleFunc("POST /auth/verify", h.verify)
	mux.HandleFunc("POST /auth/logout", h.logout)
	mux.HandleFunc("GET /auth/grant", h.grant)
}

// startData es el modelo de datos para auth/start.html.
type startData struct {
	// Session siempre nil en /auth (páginas anónimas), pero el layout lo
	// chequea para decidir si renderear el nav — el campo tiene que existir.
	Session *session.Session
	Email   string
	Error   string
	// TurnstileSiteKey vacía hace que la plantilla omita el widget.
	TurnstileSiteKey string
}

// verifyData es el modelo de datos para auth/verify.html.
type verifyData struct {
	Session *session.Session // ver comentario en startData
	Email   string
	Error   string
}

// otpEmailData es el modelo para otp.txt y otp.html.
//
// Code es el código formateado con espacio para legibilidad en plain-text
// ("123 456"). RawCode son los 6 dígitos sin espacio ("123456") para que
// el HTML lo muestre en un bloque copy-pasteable que vaya directo al
// input de /auth/verify (que rechaza espacios).
type otpEmailData struct {
	Code        string
	RawCode     string
	To          string
	IPAddress   string
	UserAgent   string
	RequestedAt string
	BaseURL     string
}

func (h *Handler) startForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := session.FromContext(r.Context()); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.render(w, r, "auth/start.html", h.startDataFor(startData{Email: r.URL.Query().Get("email")}))
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	ip := h.clientIP(r)
	if !h.startIPLim.Allow(ip) {
		h.rejectGeneric(w, r, "")
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	emailAddr := strings.TrimSpace(r.PostFormValue("email"))
	if _, err := mail.ParseAddress(emailAddr); err != nil {
		h.render(w, r, "auth/start.html", h.startDataFor(startData{
			Email: emailAddr,
			Error: "ingresa un email válido",
		}))
		return
	}

	// Reto antes de gastar cualquier presupuesto atado al email (limiter
	// por-email, buckets de envío). Va después de los limiters baratos
	// in-memory por IP y antes de todo lo que toque DB o red, para que un
	// flood se coma lo menos posible por request. token vacío corta en seco
	// dentro de Verify sin llamar a Cloudflare, así que esto no le regala
	// I/O a un flood sin token.
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

	// Limiter por-email después del reto: si corriera antes, cualquier POST
	// con una dirección ajena —sin resolver el challenge— gastaría un token
	// de esa víctima sin costo para quien ataca. Con el reto de por medio,
	// vaciar el bucket de alguien exige resolver Turnstile en cada intento.
	if !h.startEmailLim.Allow(emailAddr) {
		h.rejectGeneric(w, r, emailAddr)
		return
	}

	// Cold-list antes del presupuesto de envío: si fuera al revés, una
	// dirección quemada seguiría cobrando un token del bucket compartido
	// (known o unknown) en cada intento posterior, sin mandar nada — el
	// mismo DoS de acceso de v1.0.4 reconstruido adentro del bucket que
	// se partió justamente para no repetirlo. Ver el comentario de
	// StartKnownLimiter en Config.
	burned, err := h.unverifiedCount(r.Context(), emailAddr)
	if err != nil {
		h.logger.Error("consultar cold-list", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	if burned >= coldListThreshold {
		h.logger.Warn("dirección en cold-list: envío frenado",
			"intentos_sin_verificar", burned, "ip", ip, "email", emailAddr)
		h.rejectGeneric(w, r, emailAddr)
		return
	}

	// Presupuesto de envío partido por tipo de destinatario. Es lo último
	// que se gasta antes de generar el código y mandar el correo: todo lo
	// que puede rechazar sin tocar el bucket ya corrió arriba. Ver el
	// comentario de StartKnownLimiter en Config para el porqué del split.
	// El token no se reembolsa si el envío falla después: x/time/rate no
	// tiene un refund limpio, y el bucket se autorregenera solo. Es una
	// consecuencia documentada, no un defecto — un outage del proveedor
	// puede drenar el bucket compartido hasta que se recargue.
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
		h.rejectGeneric(w, r, emailAddr)
		return
	}

	code, err := code.Generate()
	if err != nil {
		h.logger.Error("generar código", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	ua := r.UserAgent()

	// renderOTPEmail va antes de storeCode a propósito: no necesita el id de
	// la fila, así que moverlo antes elimina la ventana en la que quedaría
	// una fila insertada sin delete si el render fallara — en vez de agregar
	// un segundo delete para cubrirla. En la práctica ese caso es casi
	// imposible (las plantillas se parsean al arrancar y los datos son un
	// struct estático), pero si fallara, fallaría igual para todos,
	// cold-listeando a cualquiera en dos intentos.
	plainBody, htmlBody, err := h.renderOTPEmail(code, emailAddr, ip, ua)
	if err != nil {
		h.logger.Error("renderizar email otp", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	codeID, err := h.storeCode(r.Context(), emailAddr, code, ip, ua)
	if err != nil {
		h.logger.Error("guardar código", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	if err := h.email.SendMessage(email.Message{
		To:        emailAddr,
		Subject:   "tu código de mokuji",
		PlainBody: plainBody,
		HTMLBody:  htmlBody,
	}); err != nil {
		h.logger.Error("enviar email", "err", err, "to", emailAddr)
		// El código ya quedó insertado, pero nunca salió: si se cuenta
		// igual, un outage del proveedor cold-listea usuarios reales por
		// 24h después de que el mail vuelva. Best-effort: si el delete
		// también falla, lo logueamos y devolvemos el 500 igual — el
		// hecho que le importa al usuario es que el envío falló.
		//
		// El delete usa context.WithoutCancel a propósito, no r.Context():
		// SendMail no tiene deadline, así que un proveedor que blackholea
		// paquetes (en vez de rechazar la conexión) cuelga el handler el
		// timeout de conexión TCP del SO — un minuto o más. El usuario
		// abandona el login antes de eso, el cliente se desconecta, y Go
		// cancela r.Context(). Con ese contexto, database/sql se niega a
		// entregar una conexión (revisa ctx.Done() antes de dar una del
		// pool) y el DELETE falla garantizado, no solo probablemente —
		// justo el escenario que este rollback existe para evitar. El
		// cleanup tiene que sobrevivir a la muerte del request precisamente
		// porque la muerte del request es lo que dispara el caso: no
		// "simplificar" esto de vuelta a r.Context().
		if delErr := h.deleteCode(context.WithoutCancel(r.Context()), codeID); delErr != nil {
			h.logger.Error("revertir código tras fallo de envío", "err", delErr, "to", emailAddr)
		}
		h.errs.InternalServerError(w, r)
		return
	}

	http.Redirect(w, r, "/auth/verify?email="+url.QueryEscape(emailAddr), http.StatusSeeOther)
}

func (h *Handler) verifyForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := session.FromContext(r.Context()); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	emailAddr := r.URL.Query().Get("email")
	if emailAddr == "" {
		http.Redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}
	h.render(w, r, "auth/verify.html", verifyData{Email: emailAddr})
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	if !h.verifyIPLim.Allow(h.clientIP(r)) {
		h.render(w, r, "auth/verify.html", verifyData{
			Email: r.URL.Query().Get("email"),
			Error: "demasiados intentos en pocos minutos. espera un poco y vuelve a intentar.",
		})
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	emailAddr := strings.TrimSpace(r.PostFormValue("email"))
	// Sanitizamos a solo dígitos: algunos clients de email pegan el código
	// con non-breaking spaces (U+00A0), tabs, o caracteres invisibles que
	// strings.TrimSpace/ReplaceAll(" ", "") no cubre. sanitizeCode robusto.
	otp := code.Sanitize(r.PostFormValue("code"))

	if _, err := mail.ParseAddress(emailAddr); err != nil {
		h.render(w, r, "auth/verify.html", verifyData{Email: emailAddr, Error: "el email no parece válido. prueba con el formato nombre@dominio."})
		return
	}
	if !code.Valid(otp) {
		h.render(w, r, "auth/verify.html", verifyData{Email: emailAddr, Error: "el código no es válido. fíjate que sean 6 dígitos sin espacios."})
		return
	}

	ok, err := h.consumeCode(r.Context(), emailAddr, otp)
	if err != nil {
		h.logger.Error("validar código", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}
	if !ok {
		h.render(w, r, "auth/verify.html", verifyData{Email: emailAddr, Error: "el código no es válido o ya expiró. vuelve a /auth para pedir uno nuevo."})
		return
	}

	// El checkbox solo aplica si este verify resulta en signup (user nuevo).
	// Para login (user existente) el valor previo se preserva intacto.
	digestOptIn := r.PostFormValue("digest_opt_in") == "on"

	userID, err := h.findOrCreateUser(r.Context(), emailAddr, digestOptIn)
	if err != nil {
		h.logger.Error("crear usuario", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	if err := h.sessions.Create(w, r, userID); err != nil {
		h.logger.Error("crear sesión", "err", err)
		h.errs.InternalServerError(w, r)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.sessions.Destroy(w, r); err != nil {
		h.logger.Error("destruir sesión", "err", err)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// render ejecuta la plantilla y la escribe a w. Si falla, loguea y responde 500.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	tmpl, ok := h.pages[page]
	if !ok {
		h.logger.Error("plantilla no encontrada", "page", page)
		h.errs.InternalServerError(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("ejecutar plantilla", "page", page, "err", err)
	}
}

// startDataFor completa los campos de startData que salen de la config y no
// del request. Centralizarlo evita que un render nuevo se olvide de la site
// key y renderice la página sin widget.
func (h *Handler) startDataFor(d startData) startData {
	d.TurnstileSiteKey = h.turnstileSiteKey
	return d
}

// rejectGeneric renderiza el rechazo compartido por los gates de rate-limit
// y cold-list de start (IP, email, bucket known/unknown, cold-list). El
// texto es a propósito palabra por palabra el mismo en los cuatro: si uno
// dijera algo distinto, filtraría cuál gate disparó, y de rebote si esa
// dirección tiene cuenta. Centralizarlo en un solo call site lo vuelve
// estructural en vez de una convención que cuatro lugares tienen que
// recordar mantener.
//
// Los rechazos de Turnstile NO pasan por acá: usan su propio texto, distinto
// entre sí (Cloudflare caído vs reto fallido) y distinto de este. Meterlos
// en este helper forzaría a unificar o parametrizar el mensaje, justo lo que
// hay que evitar para no correr el riesgo de que alguien lo colapse en el
// texto genérico sin darse cuenta.
func (h *Handler) rejectGeneric(w http.ResponseWriter, r *http.Request, emailAddr string) {
	h.render(w, r, "auth/start.html", h.startDataFor(startData{
		Email: emailAddr,
		// A propósito vago sobre el tiempo: esto también es lo que ve un
		// email en cold-list, y ahí la espera real es hasta 24h, no
		// "minutos". "espera un poco" prometía algo corto que no siempre
		// es cierto.
		Error: "demasiados intentos. espera antes de volver a intentar.",
	}))
}

// renderOTPEmail produce las dos versiones del email: texto plano (siempre)
// y HTML (solo si otp.html está cargada). El caller manda multipart cuando
// htmlBody no es vacío.
func (h *Handler) renderOTPEmail(otp, to, ip, ua string) (plainBody, htmlBody string, err error) {
	data := otpEmailData{
		Code:        code.Format(otp),
		RawCode:     otp,
		To:          to,
		IPAddress:   ip,
		UserAgent:   ua,
		RequestedAt: time.Now().UTC().Format("2006-01-02 15:04 UTC"),
		BaseURL:     h.baseURL,
	}
	var txtBuf bytes.Buffer
	if err := h.emailT.ExecuteTemplate(&txtBuf, "otp.txt", data); err != nil {
		return "", "", err
	}
	if h.emailHTMLT == nil {
		return txtBuf.String(), "", nil
	}
	var htmlBuf bytes.Buffer
	if err := h.emailHTMLT.ExecuteTemplate(&htmlBuf, "otp.html", data); err != nil {
		return "", "", err
	}
	return txtBuf.String(), htmlBuf.String(), nil
}

// storeCode inserta el código y retorna el id de la fila, para que el
// caller pueda revertirla con deleteCode si el envío falla después.
func (h *Handler) storeCode(ctx context.Context, email, otp, ip, ua string) (int64, error) {
	now := time.Now().UTC()
	expires := now.Add(codeDuration)
	res, err := h.db.ExecContext(ctx, `
		INSERT INTO otp_codes (email, code_hash, created_at, expires_at, ip_address, user_agent)
		VALUES (?, ?, ?, ?, ?, ?)
	`, email, code.Hash(otp), now.Unix(), expires.Unix(), ip, ua)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// deleteCode borra por id la fila que insertó storeCode. Existe para cuando
// SendMessage falla: un código que nunca llegó al proveedor de correo no
// puede quedar contando como intento quemado para la cold-list (ver
// unverifiedCount) — si quedara, un outage del proveedor deja a usuarios
// reales cold-listeados por 24h después de que el envío se restablezca.
func (h *Handler) deleteCode(ctx context.Context, id int64) error {
	_, err := h.db.ExecContext(ctx, `DELETE FROM otp_codes WHERE id = ?`, id)
	return err
}

// consumeCode valida y marca un código como usado. Retorna (true, nil) si fue
// válido y se consumió; (false, nil) si no había match válido; (_, err) si
// hubo error de DB.
func (h *Handler) consumeCode(ctx context.Context, email, otp string) (bool, error) {
	hash := code.Hash(otp)
	now := time.Now().UTC().Unix()

	res, err := h.db.ExecContext(ctx, `
		UPDATE otp_codes
		SET used_at = ?
		WHERE email = ? AND code_hash = ? AND expires_at > ? AND used_at IS NULL
	`, now, email, hash, now)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

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

// findOrCreateUser retorna el id del usuario con el email dado, creando uno
// nuevo si no existe. digestOptIn solo se aplica en signup (creación):
// para users existentes el valor previo de email_digest_enabled se preserva.
func (h *Handler) findOrCreateUser(ctx context.Context, emailAddr string, digestOptIn bool) (int64, error) {
	var id int64
	err := h.db.QueryRowContext(ctx,
		`SELECT id FROM users WHERE email = ?`, emailAddr,
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	digestVal := 0
	if digestOptIn {
		digestVal = 1
	}
	res, err := h.db.ExecContext(ctx,
		`INSERT INTO users (email, created_at, email_digest_enabled) VALUES (?, ?, ?)`,
		emailAddr, time.Now().UTC().Unix(), digestVal,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// grant canjea un token emitido por POST /admin/grant-session por una
// sesión real. Es la mitad pública del break-glass: sin esto, un envío de
// email caído deja el sitio inaccesible hasta para el operador.
//
// No lleva rate limit propio: el token es de 256 bits y single-use, así que
// adivinarlo no es un ataque viable, y el emisor ya está limitado.
func (h *Handler) grant(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	if token == "" {
		h.logger.Warn("canje de grant sin token", "ip", h.clientIP(r))
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

// clientIP identifica al cliente para rate limiting y logs.
func (h *Handler) clientIP(r *http.Request) string {
	return httpx.ClientIP(r, h.trustProxy)
}
