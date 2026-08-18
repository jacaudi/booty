package http

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/web"
	"github.com/spf13/viper"
)

// StartHTTP starts the HTTP server in a background goroutine and returns it so
// the caller can Shutdown() it during graceful shutdown. Signal handling and
// the ordered shutdown live with the caller; this function only starts serving.
func StartHTTP(deps APIDeps) *http.Server {
	port := fmt.Sprintf(":%d", viper.GetInt(config.HttpPort))
	slog.Info("starting HTTP server", "addr", port)
	// Create a mux for routing incoming requests
	myHandler := http.NewServeMux()

	// All URLs will be handled by this function
	myHandler.HandleFunc("/", handleRequest)
	myHandler.HandleFunc("/ignition.json", handleIgnitionRequest(deps.Store))
	myHandler.HandleFunc("/machineconfig", handleMachineConfigRequest(deps.Store))
	myHandler.HandleFunc("/preseed", handlePreseedRequest(deps.Store))
	myHandler.HandleFunc("/version.txt", handleVersionRequest)
	myHandler.HandleFunc("/version.json", handleVersionRequest)
	myHandler.HandleFunc("/hosts", handleHostsRequest)
	myHandler.HandleFunc("/register", handleRegistrationRequest)
	myHandler.HandleFunc("/unregister", handleUnregistrationRequest)
	myHandler.HandleFunc("/booty.json", handleDataRequest)
	myHandler.HandleFunc("/info", handleInfoRequest)
	myHandler.HandleFunc("/healthz", handleHealthz)
	myHandler.Handle("/data/", http.StripPrefix("/data/", dataFileHandler(viper.GetString(config.DataDir))))
	uiFS, err := web.DistFS()
	if err != nil {
		slog.Error("ui embed", "err", err)
		os.Exit(1)
	}
	myHandler.Handle("/ui/", http.StripPrefix("/ui/", uiHandler(uiFS)))

	// Mount the typed /api/v1 surface on the same mux (additive).
	RegisterAPI(myHandler, deps)

	s := &http.Server{
		Addr:           port,
		Handler:        logRequest(myHandler),
		ReadTimeout:    900 * time.Second,
		WriteTimeout:   900 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	go func() {
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
	}()
	slog.Info("server started")

	return s
}

// dataFileHandler serves files under dataDir, restricted to the boot-artifact
// cache subtree (see isAllowedDataPath) and blocking any request whose
// (decoded) path targets an in-flight staged download (see isPartialPath).
// Extracted from StartHTTP's /data/ registration so it is independently
// testable via httptest without standing up the full mux/server.
//
// Serving continues to go through http.FileServer so conditional and ranged
// GETs keep working — iPXE and the multi-GB ISO paths depend on Range.
func dataFileHandler(dataDir string) http.Handler {
	dataFS := http.FileServer(http.Dir(dataDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedDataPath(r.URL.Path) || isPartialPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		dataFS.ServeHTTP(w, r)
	})
}

// isAllowedDataPath reports whether a /data/-relative request path lies inside
// the boot-artifact cache subtree. Everything else under dataDir 404s.
//
// This is an ALLOWLIST rather than a denylist because dataDir is not an
// artifact-only directory: config.DatabasePathValue puts booty.db (plus its
// -wal and -shm siblings) there by default, and deploy/docker-compose.yml
// mounts one volume for the lot. A denylist served the SQLite database — and
// with it config_revisions.source_b64, the plaintext per-host boot configs —
// to any unauthenticated caller, and would have to be extended for every new
// file anything ever writes to dataDir. The cache tree, by contrast, is the
// only subtree served content references: every /data/ URL booty emits is
// built by cache.CacheURLPath, which hardcodes "/data/cache/".
//
// Blocking the dataDir root also suppresses FileServer's directory listing of
// it, which by itself disclosed the database filenames. The cache root is
// likewise excluded — strictly UNDER it is allowed, not the directory itself —
// because no emitted URL names it (they all reach a file under
// <os>/<schematic>/<arch>/<version>/), so its listing would enumerate every
// cached OS and version for nothing.
//
// Traversal is handled the same way pkg/tftp's safeJoin does it — clean first,
// then require the cleaned result to be under the root — so "cache/.." escapes
// are refused rather than normalized into a served path. The refusal is a
// plain 404 so it is indistinguishable from a miss (FileServer's own dot-dot
// rejection is a 400, which would leak the difference).
func isAllowedDataPath(p string) bool {
	// root is the last segment of cache.CacheURLPath's "/data/cache/" prefix,
	// and must stay in step with pkg/cache's cacheRoot (<dataDir>/cache);
	// pkg/http cannot import pkg/cache for it without an import cycle.
	const root = "/cache"
	// StripPrefix leaves the path without a leading slash; path.Clean needs the
	// leading one to resolve "..", collapses the doubled slash when p already
	// had it, and always returns a rooted, slash-separated result.
	cleaned := path.Clean("/" + p)
	return strings.HasPrefix(cleaned, root+"/")
}

// isPartialPath reports whether a request path targets an in-flight download.
// Such files must never be served (they are incomplete/unverified); the boot
// path never references them, this guards direct /data/ browsing.
//
// Both in-progress suffixes are covered. ".partial" is the staged downloader's
// (pkg/cache/verify.go); ".download" is the resumable large-file downloader's
// (pkg/cache/isodownload.go), which uses a different suffix precisely so
// SweepPartials cannot delete a multi-GB ISO between reconcile ticks. That
// makes a .download file the LONGER-lived of the two on disk — a stalled
// multi-GB transfer can sit for hours by design — inside exactly the cache
// directory [[baseurl]] points at.
//
// Case-insensitive: both suffixes are always written lowercase, but on a
// case-insensitive dev filesystem (e.g. macOS/APFS) a request for
// "kernel.PARTIAL" would otherwise resolve to the same on-disk file and
// bypass a case-sensitive check.
func isPartialPath(p string) bool {
	lower := strings.ToLower(p)
	return strings.HasSuffix(lower, ".partial") || strings.HasSuffix(lower, ".download")
}

func logRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request", "remote", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
		handler.ServeHTTP(w, r)
	})
}
