// Package server — handlers para discoverability técnica (SEO + LLM-friendly
// indexing). Se sirven en el root del dominio igual que /sw.js y
// /manifest.webmanifest (ver mountPWA en routes.go).
package server

import (
	"fmt"
	"net/http"
)

// mountDiscoverability registra los endpoints que ayudan a crawlers y LLMs
// a indexar mokuji correctamente:
//
//   - /robots.txt    estándar de exclusión de crawlers
//   - /sitemap.xml   mapa de URLs públicas
//   - /llms.txt      descripción Markdown del proyecto para LLMs (llmstxt.org)
//
// baseURL es el origen público (ej. "https://mokuji.org") usado para emitir
// URLs absolutas en sitemap y robots.
func mountDiscoverability(mux *http.ServeMux, baseURL string) {
	mux.HandleFunc("GET /robots.txt", serveRobotsTxt(baseURL))
	mux.HandleFunc("GET /sitemap.xml", serveSitemap(baseURL))
	mux.HandleFunc("GET /llms.txt", serveLLMsTxt)
}

func serveRobotsTxt(baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fmt.Fprintf(w, "User-agent: *\nAllow: /\n\nSitemap: %s/sitemap.xml\n", baseURL)
	}
}

func serveSitemap(baseURL string) http.HandlerFunc {
	const tpl = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url>
    <loc>%s/</loc>
    <changefreq>weekly</changefreq>
    <priority>1.0</priority>
  </url>
</urlset>
`
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fmt.Fprintf(w, tpl, baseURL)
	}
}

const llmsTxtBody = `# mokuji

Una biblioteca pública y self-hostable para tu manga. Sin algoritmos, sin
recomendaciones — solo un índice limpio de lo que importa.

## Qué hace

- **Biblioteca privada por user**: cataloga títulos con estado leyendo / por
  leer / completado, marca favoritos, exporta a JSON cuando quieras.
- **Tracking de releases multi-source**: matchea automáticamente contra
  MangaDex, Asura Scans y Comick. Cuando hay capítulo nuevo, lo ves en
  ` + "`/releases`" + ` y opcionalmente por email.
- **Digest opt-in**: semanal por default (domingos), diario si lo eliges, o
  apagado. Nunca predatory.
- **Perfiles públicos** en ` + "`/u/{handle}`" + `: si el user marca el perfil
  como público, su lista es visible sin login.

## Stack

Go + SQLite, stdlib ` + "`net/http`" + ` puro, ` + "`html/template`" + `
server-rendered, sin frameworks. Single binary, ~13 MB stripped, sin CGO.
PWA instalable. Email outbound vía SMTP estándar.

## Licencia

[AGPL-3.0](https://github.com/odin-software/mokuji/blob/main/LICENSE).
Si corres una instancia pública, debes ofrecer el código a tus users.

## Recursos

- Instancia pública: <https://mokuji.org>
- Código fuente: <https://github.com/odin-software/mokuji>
- Self-hosting: <https://github.com/odin-software/mokuji/blob/main/docs/self-hosting.md>
- Filosofía y decisiones: <https://github.com/odin-software/mokuji/tree/main/docs/adr>
`

func serveLLMsTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte(llmsTxtBody))
}
