package http

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/cache"
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

// dataFileHandler serves files under dataDir, restricted to the subtrees in
// dataSubtrees (see isAllowedDataPath) and blocking any request whose path
// targets an in-flight staged download (see isPartialPath).
//
// Serving goes through http.FileServer so conditional and ranged GETs keep
// working — iPXE and the multi-GB ISO paths depend on Range — but over a
// listing-free FS, so a directory is indistinguishable from a miss.
func dataFileHandler(dataDir string) http.Handler {
	dataFS := http.FileServer(noListingDir{http.Dir(dataDir)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Both predicates must judge the SAME path http.FileServer will open,
		// which is path.Clean(r.URL.Path) (net/http/fs.go:995). Testing the raw
		// path let a trailing "/%2e" name an in-flight file while defeating the
		// suffix check — ServeMux cleans the literal "/." spelling away before
		// dispatch, but not the encoded one.
		cleaned := path.Clean("/" + r.URL.Path)
		if !isAllowedDataPath(cleaned) || isPartialPath(cleaned) {
			http.NotFound(w, r)
			return
		}
		dataFS.ServeHTTP(w, r)
	})
}

// dataSubtrees are the only subtrees of dataDir that /data/ serves: the boot
// artifact cache, and "public" for operator assets the BOOTED NODE fetches over
// HTTP (the shell scripts examples/config/ignition.yaml points at). "public"
// exists so must-serve assets stop sharing <dataDir>/config/ with the
// ignition/machineconfig/preseed templates booty renders server-side, which
// must never be published.
//
// Serving dataDir wholesale published booty.db — config.DatabasePathValue
// defaults it there and deploy/docker-compose.yml mounts one volume for the lot
// — including config_revisions.source_b64, the plaintext per-host boot configs.
// An allowlist is the only form that does not need extending for every new file
// something writes to dataDir.
var dataSubtrees = []string{"/" + cache.DirName, "/public"}

// isAllowedDataPath reports whether a /data/-relative request path lies inside
// one of dataSubtrees. Everything else under dataDir 404s.
//
// Matching is strictly BELOW each subtree, so the subtree roots are not
// addressable either. Cleaning first, then prefix-matching, is the same idiom
// as pkg/tftp's safeJoin (tftp.go:75-80) — and it inherits the same caveat
// safeJoin documents: no EvalSymlinks, so a symlink planted inside an allowed
// subtree whose target lies outside it is followed. Nothing booty writes
// creates one (the cache downloaders write regular files only) and the operator
// owns dataDir, so this is the same accepted limitation, not a new one. Closing
// it structurally means os.OpenRoot, which pins an fd to the directory inode
// for process lifetime; that is a bigger change than this guard warrants.
func isAllowedDataPath(p string) bool {
	// path.Clean needs a leading slash to resolve ".." (StripPrefix leaves none)
	// and collapses a doubled one when p already had it.
	cleaned := path.Clean("/" + p)
	for _, root := range dataSubtrees {
		if strings.HasPrefix(cleaned, root+"/") {
			return true
		}
	}
	return false
}

// noListingDir is http.Dir with directory opens refused, so http.FileServer
// renders no index and cannot answer differently for a directory than for a
// miss. The listing under a cached version directory named the in-flight
// .partial/.download files by hand-browsable path, which is the discovery half
// of fetching one; the 301-on-directory behaviour was also an existence oracle
// for paths the allowlist otherwise hides.
type noListingDir struct{ http.Dir }

func (d noListingDir) Open(name string) (http.File, error) {
	f, err := d.Dir.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
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
