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
