// Package errrender expone ErrorRenderer: renderizador de páginas de error
// estilizadas (404 y 500) que usan el mismo layout/CSS que el resto de la app.
//
// El paquete vive fuera de internal/server para que los handlers de features
// (home, library, auth, …) puedan recibirlo como dependencia sin crear ciclos
// de import (server importa a los handlers; los handlers no pueden importar
// server).
package errrender

import (
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/odin-software/mokuji/internal/csrf"
	"github.com/odin-software/mokuji/internal/feedback"
	"github.com/odin-software/mokuji/internal/session"
	"github.com/odin-software/mokuji/internal/version"
)

// ErrorRenderer renderiza las páginas 404 y 500 usando el layout principal
// (mismo header/footer/CSS que el resto de la app). Se construye una vez en
// main.go y se inyecta en server.Server y en cada handler user-facing.
type ErrorRenderer struct {
	tmpl404 *template.Template
	tmpl500 *template.Template
	logger  *slog.Logger
}

// New parsea las plantillas. Si htmlFS es nil retorna nil sin error: en tests
// sin assets los handlers caen al fallback plano de net/http.
func New(htmlFS fs.FS, logger *slog.Logger) (*ErrorRenderer, error) {
	if htmlFS == nil {
		return nil, nil
	}
	funcs := template.FuncMap{
		"appVersion":      func() string { return version.Value },
		"csrfInput":       csrf.Input,
		"feedbackEnabled": feedback.IsEnabled,
	}
	tmpl404, err := template.New("base").Funcs(funcs).ParseFS(htmlFS, "layout.html", "404.html")
	if err != nil {
		return nil, fmt.Errorf("parsear 404: %w", err)
	}
	tmpl500, err := template.New("base").Funcs(funcs).ParseFS(htmlFS, "layout.html", "500.html")
	if err != nil {
		return nil, fmt.Errorf("parsear 500: %w", err)
	}
	return &ErrorRenderer{tmpl404: tmpl404, tmpl500: tmpl500, logger: logger}, nil
}

// NotFound responde 404 con la página estilizada. Se monta como catch-all en
// el mux: cualquier path no registrado cae acá. La sesión (si existe) se pasa
// para que el layout muestre la nav autenticada.
func (e *ErrorRenderer) NotFound(w http.ResponseWriter, r *http.Request) {
	sess, _ := session.FromContext(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	if err := e.tmpl404.ExecuteTemplate(w, "layout", map[string]any{"Session": sess}); err != nil {
		e.logger.Error("renderizar 404", "err", err, "path", r.URL.Path)
	}
}

// ServerError responde 500 con la página estilizada. errorID es el request ID
// (cuando viene del recoverer) para que el usuario lo pueda copiar y abrir
// un issue. requestedAt se renderiza en RFC3339 UTC.
func (e *ErrorRenderer) ServerError(w http.ResponseWriter, r *http.Request, errorID string) {
	sess, _ := session.FromContext(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	data := map[string]any{
		"Session":     sess,
		"ErrorID":     errorID,
		"RequestedAt": time.Now().UTC().Format(time.RFC3339),
	}
	if err := e.tmpl500.ExecuteTemplate(w, "layout", data); err != nil {
		e.logger.Error("renderizar 500", "err", err, "path", r.URL.Path)
	}
}

// InternalServerError responde con 500 estilado vía 500.html en vez de texto
// plano. Usar desde handlers cuando un error opaco (DB caída, SMTP timeout,
// etc.) impide servir la respuesta normal. El logger del caller ya capturó
// el error específico; esta función solo se encarga del rendering al usuario.
//
// Si el renderer es nil (tests sin assets), cae al texto plano de net/http.
func (e *ErrorRenderer) InternalServerError(w http.ResponseWriter, r *http.Request) {
	if e == nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sess, _ := session.FromContext(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	data := map[string]any{
		"Session":     sess,
		"RequestedAt": time.Now().UTC().Format(time.RFC3339),
	}
	if err := e.tmpl500.ExecuteTemplate(w, "layout", data); err != nil {
		// Fallback a texto plano si el template falla.
		e.logger.Error("renderizar 500", "err", err, "path", r.URL.Path)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
