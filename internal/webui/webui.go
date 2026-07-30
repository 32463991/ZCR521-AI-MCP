// Package webui serves the dependency-free, read-only status page.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed index.html assets/*
var content embed.FS

// New returns a handler that exposes only the status page and its two static
// assets. All live values come from the same-origin /status endpoint.
func New() http.Handler {
	assets, err := fs.Sub(content, "assets")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; "+
					"img-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'")
			data, readErr := content.ReadFile("index.html")
			if readErr != nil {
				http.Error(w, "status page unavailable", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(data)
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimPrefix(path.Clean(r.URL.Path), "/assets/")
			if name != "status.css" && name != "status.js" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=3600")
			clone := r.Clone(r.Context())
			clone.URL.Path = "/" + name
			fileServer.ServeHTTP(w, clone)
		default:
			http.NotFound(w, r)
		}
	})
}
