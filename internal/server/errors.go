package server

import (
	"io/fs"
	"log/slog"

	"git.odin.do/kengru/mokuji/internal/errrender"
)

// errorRenderer es un alias local para facilitar el acceso al tipo del paquete
// errrender desde el resto del paquete server (middleware, routes).
type errorRenderer = errrender.ErrorRenderer

// newErrorRenderer delega al constructor canónico en errrender. Si htmlFS es
// nil retorna nil sin error (tests sin assets).
func newErrorRenderer(htmlFS fs.FS, logger *slog.Logger) (*errorRenderer, error) {
	return errrender.New(htmlFS, logger)
}
