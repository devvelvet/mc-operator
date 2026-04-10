package api

import (
	"embed"
	"io/fs"
	"net/http"
)

// webFS holds the embedded dashboard assets.
// The //go:embed directive requires the files to exist at compile time.
//
//go:embed all:static
var webFS embed.FS

// WebHandler returns an http.Handler that serves the embedded dashboard.
// Requests for files that don't exist fall back to index.html, enabling
// client-side routing.
func WebHandler() http.Handler {
	sub, err := fs.Sub(webFS, "static")
	if err != nil {
		// At compile time the directory must exist, so this should never fire
		// at runtime unless someone deleted the embed tag.
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If the requested path doesn't exist in the embed FS, serve index.html
		// so the SPA can handle routing itself.
		if r.URL.Path != "/" {
			if _, err := fs.Stat(sub, r.URL.Path[1:]); err != nil {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/"
				fileServer.ServeHTTP(w, r2)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}
