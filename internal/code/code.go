// Package code maneja códigos OTP de 6 dígitos: generación crypto-random,
// sanitización defensiva del input del user, validación de formato, hash
// SHA-256 para almacenamiento, formateo para email.
//
// Reusado por internal/auth (login OTP) e internal/profile (confirmación
// de borrado de cuenta). Ambos consumidores definen sus propias duraciones
// y tablas; este paquete solo provee el primitivo del código.
package code

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
)

// Length es la longitud del código en dígitos.
const Length = 6

// Generate produce un código de 6 dígitos numéricos crypto-random.
func Generate() (string, error) {
	var code [Length]byte
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		code[i] = byte('0' + n.Int64())
	}
	return string(code[:]), nil
}

// Format inserta un espacio en el medio para legibilidad en email:
// "123456" → "123 456". Si len(s) != Length, devuelve s sin tocar.
func Format(s string) string {
	if len(s) != Length {
		return s
	}
	return s[:3] + " " + s[3:]
}

// Valid retorna true si s es exactamente Length dígitos.
func Valid(s string) bool {
	if len(s) != Length {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Sanitize extrae solo los dígitos del input. Más tolerante que un
// TrimSpace+ReplaceAll: cubre non-breaking spaces (U+00A0) que Gmail/Apple
// Mail meten al renderear "123 456", tabs, zero-width chars, etc. Devuelve
// los dígitos en orden; el caller chequea longitud con Valid.
func Sanitize(s string) string {
	out := make([]byte, 0, Length)
	for _, r := range s {
		if r >= '0' && r <= '9' {
			out = append(out, byte(r))
		}
	}
	return string(out)
}

// Hash devuelve SHA-256(s) en hex. Usado para almacenar el código en DB
// sin guardar el plaintext.
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
