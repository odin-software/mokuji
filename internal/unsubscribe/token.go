// Package unsubscribe implementa tokens HMAC firmados para opt-out de emails
// sin requerir sesión (ADR 0013 §6, RFC 8058 List-Unsubscribe-Post).
//
// Modelo: el servidor firma un payload `userID:scope:issuedAt` con HMAC-SHA256
// y un secret server-side (UNSUBSCRIBE_SECRET). El token viaja en el link de
// cada email; cuando el user lo abre, el endpoint /unsubscribe re-firma el
// payload y compara la firma en tiempo constante. No guardamos el token en DB
// — el secret + el contenido firmado son la única "credencial". Si el secret
// rota, todos los tokens previos quedan inválidos automáticamente (aceptable:
// el user puede pedir otro link desde /settings).
//
// El scope permite reutilizar el mismo signer para distintos tipos de emails
// en el futuro (ej. `releases_digest`, `weekly_summary`) sin que un token de
// uno valga para otro.
package unsubscribe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Scopes válidos para los tokens firmados. Cada scope tiene su propio
// payload — un token de un scope no vale para otro porque la verificación
// chequea el payload completo (formato + scope incluido).
const (
	// ScopeReleasesDigest cubre el opt-out del digest semanal. Payload:
	// "userID:scope:issuedAt". MaxAge = MaxAge.
	ScopeReleasesDigest = "releases_digest"
	// ScopeReadChapter cubre el link "leer cap" del digest: marca leído +
	// redirige al lector. Payload: "userID:chapterID:scope:issuedAt".
	// MaxAge = MaxAgeReadChapter (más corto: si el user no abre el email
	// en 30 días, el cap ya no es novedad).
	ScopeReadChapter = "read_chapter"
)

// MaxAge es la edad máxima aceptada de un token de unsubscribe desde
// issuedAt. Tokens más viejos se rechazan: si un user no abre el email en
// 90 días, mandamos uno nuevo con token fresco. Cota holgada para no joder
// a quien revisa email con baja frecuencia, pero acota la ventana de un
// token filtrado.
const MaxAge = 90 * 24 * time.Hour

// MaxAgeReadChapter es la ventana de validez del link "leer cap" del email.
// Más corta que MaxAge porque la utilidad expira: un email de digest de
// hace 31 días tiene caps que ya son viejos. Si el token vence, el user
// puede igual entrar a la app y leer desde /library normalmente.
const MaxAgeReadChapter = 30 * 24 * time.Hour

var (
	// ErrInvalidToken cubre tokens mal formados, con firma incorrecta, o con
	// scope distinto al esperado. No distinguimos entre los tres en el error
	// público para no revelar señal a un atacante haciendo fuzzing.
	ErrInvalidToken = errors.New("token inválido")
	// ErrExpiredToken se devuelve cuando el token tiene más de MaxAge.
	ErrExpiredToken = errors.New("token expirado")
)

// Signer firma y verifica tokens de unsubscribe. Construir con NewSigner.
type Signer struct {
	secret []byte
	now    func() time.Time // inyectable para tests
}

// NewSigner construye un Signer. El secret debe tener al menos 32 bytes
// (recomendado: rand 32 bytes en base64). Falla si es más corto — un secret
// débil rompe la garantía de HMAC.
func NewSigner(secret []byte) (*Signer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("UNSUBSCRIBE_SECRET muy corto: %d bytes, mínimo 32", len(secret))
	}
	return &Signer{secret: secret, now: time.Now}, nil
}

// SetNowForTesting reemplaza la función de "ahora" del signer. Solo para tests
// — permite firmar tokens con timestamps arbitrarios para validar expiración.
func (s *Signer) SetNowForTesting(fn func() time.Time) { s.now = fn }

// Sign produce un token para el (userID, scope) dado, marcado con el momento
// actual. Formato: base64url(userID:scope:issuedAt).base64url(hmac).
func (s *Signer) Sign(userID int64, scope string) string {
	issuedAt := s.now().UTC().Unix()
	payload := fmt.Sprintf("%d:%s:%d", userID, scope, issuedAt)
	sig := s.hmac(payload)
	return encode([]byte(payload)) + "." + encode(sig)
}

// Verify decodifica el token, comprueba la firma en tiempo constante, valida
// el scope esperado y la edad. Retorna el userID embebido en el payload.
func (s *Signer) Verify(token, expectedScope string) (int64, error) {
	payloadEnc, sigEnc, ok := strings.Cut(token, ".")
	if !ok {
		return 0, ErrInvalidToken
	}

	payload, err := decode(payloadEnc)
	if err != nil {
		return 0, ErrInvalidToken
	}
	sig, err := decode(sigEnc)
	if err != nil {
		return 0, ErrInvalidToken
	}

	want := s.hmac(string(payload))
	if !hmac.Equal(sig, want) {
		return 0, ErrInvalidToken
	}

	// payload = "userID:scope:issuedAt"
	parts := strings.Split(string(payload), ":")
	if len(parts) != 3 {
		return 0, ErrInvalidToken
	}
	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || userID <= 0 {
		return 0, ErrInvalidToken
	}
	if parts[1] != expectedScope {
		return 0, ErrInvalidToken
	}
	issuedAt, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, ErrInvalidToken
	}
	if s.now().UTC().Unix()-issuedAt > int64(MaxAge.Seconds()) {
		return 0, ErrExpiredToken
	}

	return userID, nil
}

// SignReadChapter produce un token que autoriza marcar el cap leído + abrir
// el lector externo desde un link en el email digest. Formato del payload:
// "userID:chapterID:scope:issuedAt".
func (s *Signer) SignReadChapter(userID, chapterID int64) string {
	issuedAt := s.now().UTC().Unix()
	payload := fmt.Sprintf("%d:%d:%s:%d", userID, chapterID, ScopeReadChapter, issuedAt)
	sig := s.hmac(payload)
	return encode([]byte(payload)) + "." + encode(sig)
}

// VerifyReadChapter es el inverso de SignReadChapter. Devuelve (userID,
// chapterID) si la firma + scope + edad son válidas.
func (s *Signer) VerifyReadChapter(token string) (userID, chapterID int64, err error) {
	payloadEnc, sigEnc, ok := strings.Cut(token, ".")
	if !ok {
		return 0, 0, ErrInvalidToken
	}

	payload, err := decode(payloadEnc)
	if err != nil {
		return 0, 0, ErrInvalidToken
	}
	sig, err := decode(sigEnc)
	if err != nil {
		return 0, 0, ErrInvalidToken
	}

	want := s.hmac(string(payload))
	if !hmac.Equal(sig, want) {
		return 0, 0, ErrInvalidToken
	}

	// payload = "userID:chapterID:scope:issuedAt"
	parts := strings.Split(string(payload), ":")
	if len(parts) != 4 {
		return 0, 0, ErrInvalidToken
	}
	userID, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil || userID <= 0 {
		return 0, 0, ErrInvalidToken
	}
	chapterID, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || chapterID <= 0 {
		return 0, 0, ErrInvalidToken
	}
	if parts[2] != ScopeReadChapter {
		return 0, 0, ErrInvalidToken
	}
	issuedAt, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return 0, 0, ErrInvalidToken
	}
	if s.now().UTC().Unix()-issuedAt > int64(MaxAgeReadChapter.Seconds()) {
		return 0, 0, ErrExpiredToken
	}
	return userID, chapterID, nil
}

func (s *Signer) hmac(payload string) []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

func encode(b []byte) string          { return base64.RawURLEncoding.EncodeToString(b) }
func decode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
