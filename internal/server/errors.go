package server

import (
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"git.odin.do/kengru/mokuji/internal/csrf"
	"git.odin.do/kengru/mokuji/internal/session"
	"git.odin.do/kengru/mokuji/internal/version"
)

// errorRenderer renderiza las páginas 404 y 500 usando el layout principal
// (mismo header/footer/CSS que el resto de la app). Se construye en el setup
// del servidor y se invoca desde el catch-all del mux (404) y desde el
// recoverer middleware (500).
type errorRenderer struct {
	tmpl404 *template.Template
	tmpl500 *template.Template
	logger  *slog.Logger
}

// newErrorRenderer parsea las plantillas. Si htmlFS es nil retorna nil sin
// error: en tests sin assets el servidor sigue funcionando con el 404 plano
// de net/http y el 500 plano del recoverer.
func newErrorRenderer(htmlFS fs.FS, logger *slog.Logger) (*errorRenderer, error) {
	if htmlFS == nil {
		return nil, nil
	}
	funcs := template.FuncMap{
		"appVersion": func() string { return version.Value },
		"csrfInput":  csrf.Input,
	}
	tmpl404, err := template.New("base").Funcs(funcs).ParseFS(htmlFS, "layout.html", "404.html")
	if err != nil {
		return nil, fmt.Errorf("parsear 404: %w", err)
	}
	tmpl500, err := template.New("base").Funcs(funcs).ParseFS(htmlFS, "layout.html", "500.html")
	if err != nil {
		return nil, fmt.Errorf("parsear 500: %w", err)
	}
	return &errorRenderer{tmpl404: tmpl404, tmpl500: tmpl500, logger: logger}, nil
}

// NotFound responde 404 con la página estilizada. Se monta como catch-all en
// el mux: cualquier path no registrado cae acá. La sesión (si existe) se pasa
// para que el layout muestre la nav autenticada.
func (e *errorRenderer) NotFound(w http.ResponseWriter, r *http.Request) {
	sess, _ := session.FromContext(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	if err := e.tmpl404.ExecuteTemplate(w, "layout", map[string]any{"Session": sess}); err != nil {
		// Ya escribimos el 404 header; logueamos y dejamos que el body quede
		// truncado antes de romper más cosas.
		e.logger.Error("renderizar 404", "err", err, "path", r.URL.Path)
	}
}

// ServerError responde 500 con la página estilizada. errorID es el request ID
// (cuando viene del recoverer) para que el usuario lo pueda copiar y abrir
// un issue. requestedAt se renderiza en RFC3339 UTC.
func (e *errorRenderer) ServerError(w http.ResponseWriter, r *http.Request, errorID string) {
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

