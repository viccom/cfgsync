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
//   - /             → dist/index.html
//   - /assets/*     → files under dist/assets/ (if present in the embed);
//     paths under assets/ that miss → 404 (NOT the SPA
//     fallback, so a typo in a <script src> doesn't get
//     silently swallowed as index.html-as-JS).
//   - anything else → dist/index.html (SPA fallback for client-side routing)
//   - /api/*        → NOT served here; mount the API handler separately.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// embed.FS.Sub only fails on programmer error; the dist/ path is hard-coded.
		panic("webui: fs.Sub: " + err.Error())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			serveIndex(w, r, sub)
			return
		}
		f, err := sub.Open(path)
		if err == nil {
			defer f.Close()
			serveFile(w, r, f)
			return
		}
		// Asset miss → 404 (don't serve HTML where JS/CSS is expected).
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			http.NotFound(w, r)
			return
		}
		// Unknown path → SPA fallback so client-side routes work on direct hit / refresh.
		serveIndex(w, r, sub)
	})
}

// serveIndex serves dist/index.html via http.ServeContent so the browser
// gets Content-Type, If-Modified-Since, and range handling for free.
func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	f, err := sub.Open("index.html")
	if err != nil {
		http.Error(w, "ui not built", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	serveFile(w, r, f)
}

// serveFile serves an embedded asset via http.ServeContent. embed.FS files
// implement io.ReadSeeker (backed by bytes.Reader), so Content-Type inference
// (from the file extension), conditional requests, and range requests are all
// handled by the standard library.
func serveFile(w http.ResponseWriter, r *http.Request, f fs.File) {
	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "stat", http.StatusInternalServerError)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		// embed.FS always returns a ReadSeeker; this guard exists only to
		// fail loudly if the embed contract changes.
		http.Error(w, "unsupported file type", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), rs)
}
