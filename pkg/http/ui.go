package http

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// uiHandler serves the embedded single-page app. Real files are served directly
// (correct MIME via http.FileServer). A request whose path has no file
// extension is treated as a client-side route and falls back to index.html; a
// missing path that DOES have an extension is a genuine 404 (so a broken asset
// reference surfaces as an error instead of silently returning the HTML shell).
//
// The fs.FS is injected so production wires the real embed (web.DistFS) while
// tests inject an in-memory fixture.
func uiHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqPath := strings.TrimPrefix(r.URL.Path, "/")
		if reqPath == "" {
			serveIndex(w, r, fsys)
			return
		}
		if _, err := fs.Stat(fsys, reqPath); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		if path.Ext(reqPath) == "" {
			serveIndex(w, r, fsys) // client-side route
			return
		}
		http.NotFound(w, r) // missing asset — do not mask as the SPA shell
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}
