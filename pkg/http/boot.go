package http

import (
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Ported from jeefy/booty pkg/server/http.go @ 639de71 (MIT).

const (
	bootPathPrefix = "/boot/"
	efiContentType = "application/efi"
)

// withBootRoute sends every request under /boot/ to bootFileHandler and the
// rest to next. It runs ahead of the mux because the mux redirects paths with
// repeated slashes (301), which firmware and iPXE do not follow.
func withBootRoute(files fs.FS, next http.Handler) http.Handler {
	boot := bootFileHandler(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, bootPathPrefix) {
			boot.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bootFileHandler serves the embedded iPXE binaries at /boot/<name>. It is raw
// on purpose: repeated slashes are collapsed without a redirect, HEAD is
// answered with the same headers as GET, Content-Length is always set, .efi
// files are application/efi (UEFI firmware needs that or the suffix) and
// nothing is listed. A nil files serves nothing.
func bootFileHandler(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed", nil)
			return
		}
		clean := path.Clean(r.URL.Path)
		if files == nil || !strings.HasPrefix(clean, bootPathPrefix) ||
			strings.HasSuffix(r.URL.Path, "/") || slices.Contains(strings.Split(r.URL.Path, "/"), "..") {
			writeError(w, http.StatusNotFound, "not found", nil)
			return
		}
		name := strings.TrimPrefix(clean, bootPathPrefix)
		data, err := fs.ReadFile(files, name)
		if err != nil {
			writeError(w, http.StatusNotFound, "not found", nil)
			return
		}
		contentType := "application/octet-stream"
		if strings.EqualFold(path.Ext(name), ".efi") {
			contentType = efiContentType
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		//nolint:gosec // G705: data is an embedded binary served as application/efi or octet-stream, never HTML
		if _, werr := w.Write(data); werr != nil {
			//nolint:sloglint // default logger: no injected logger in this package yet
			slog.Debug("writing boot file failed", "name", name, "err", werr)
		}
	})
}
