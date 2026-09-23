// Package webui embeds the purchasing dashboard into the backend binary.
package webui

import (
	"embed"
	"net/http"
)

//go:embed public/*
var assets embed.FS

// Register serves only explicit public assets; API data remains authenticated.
func Register(mux *http.ServeMux) {
	for route, file := range map[string]string{
		"GET /{$}":               "index.html",
		"GET /assets/app.js":     "app.js",
		"GET /assets/styles.css": "styles.css",
	} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
			w.Header().Set("Referrer-Policy", "same-origin")
			http.ServeFileFS(w, r, assets, "public/"+file)
		})
	}
}

// IsPublic identifies the exact frontend paths that do not require an API key.
func IsPublic(path string) bool {
	return path == "/" || path == "/assets/app.js" || path == "/assets/styles.css"
}
