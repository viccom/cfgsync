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
// - /             → dist/index.html
// - /assets/*     → files under dist/assets/ (if present in the embed);
//                   paths under assets/ that miss → 404 (NOT the SPA
//                   fallback, so a typo in a <script src> doesn't get
//                   silently swallowed as index.html-as-JS).
// - anything else → dist/index.html (SPA fallback for client-side routing)
// - /api/*        → NOT served here; mount the API handler separately.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// embed.FS.Sub only fails on programmer error; the dist/ path is hard-coded.
		panic("webui: fs.Sub: " + err.Error())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			serveIndex(w, sub)
			return
		}
		// Open the file directly; embed.FS's Stat/Content-Type come from
		// the returned fs.File. Single stat per request, no FileServer.
		f, err := sub.Open(path)
		if err == nil {
			defer f.Close()
			serveFile(w, f)
			return
		}
		// Asset miss → 404 (don't serve HTML where JS/CSS is expected).
		if strings.HasPrefix(r.URL.Path, "/assets/") {
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

func serveFile(w http.ResponseWriter, f fs.File) {
	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "stat", http.StatusInternalServerError)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "not seekable", http.StatusInternalServerError)
		return
	}
	// http.ServeContent handles Content-Type sniffing, Range requests, and Last-Modified.
	http.ServeContent(w, nil, stat.Name(), stat.ModTime(), rs)
}
