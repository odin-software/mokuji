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
