package library

import "strconv"

// FormatChapter formatea el cursor de lectura para mostrarlo: 12 → "12",
// 12.5 → "12.5" (nunca "12.000000") y 0 → "" (sin progreso no se muestra
// nada; el cursor NULL llega como 0).
func FormatChapter(n float64) string {
	if n == 0 {
		return ""
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}
