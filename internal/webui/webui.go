// Package webui embeds the static SPA into the Go binary and serves it
// with a fallback to index.html for client-side routing.
package webui

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded SPA.
// - /               → dist/index.html
// - /assets/*       → files under dist/assets/ (if present in the embed)
// - anything else   → dist/index.html (SPA fallback for client-side routing)
// - /api/*          → NOT served here; mount the API handler separately.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// embed.FS.Sub only fails on programmer error; the dist/ path is hard-coded.
		panic("webui: fs.Sub: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			serveIndex(w, sub)
			return
		}
		// If the requested file exists in the embed, serve it.
		if _, err := fs.Stat(sub, path); err == nil {
			files.ServeHTTP(w, r)
			return
		}
		// Paths under assets/ that miss → 404 (don't SPA-fallback, since the browser
		// would try to parse index.html as a script/stylesheet and produce a confusing error).
		if strings.HasPrefix(path, "assets/") {
			http.NotFound(w, r)
			return
		}
		// Unknown path → SPA fallback so client-side routes work on direct hit / refresh.
		serveIndex(w, sub)
	})
}

func serveIndex(w http.ResponseWriter, sub fs.FS) {
	f, err := sub.Open("index.html")
	if err != nil {
		http.Error(w, "ui not built", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.Copy(w, f)
}
