// Package http is booty's HTTP server: the management API under /api/v1,
// the read-only boot endpoints (iPXE, Ignition, machineconfig, preseed), the
// node health report receiver and the embedded web UI.
package http

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/dataroot"
	"github.com/jeefy/booty/web"
)

// baseMux builds the plain (non-Huma) HTTP surface: the boot-config
// endpoints (ignition/machineconfig/preseed), the open /login and /logout
// endpoints, the UI, and the /data/ artifact server. It is the single source
// of the base-mux routes, so a test asserting a route is mounted (or
// deliberately retired and no longer mounted) exercises the exact same
// registration StartHTTP uses instead of a hand-built stand-in mux that
// could silently drift from it.
//
// /hosts, /register, /unregister, /booty.json and /info were retired here:
// they are superseded by the gated /api/v1 surface (POST /api/v1/hosts,
// DELETE /api/v1/hosts/{mac}, GET /api/v1/hosts's discovered field, GET
// /api/v1/info).
//
// dataRoot is the [os.Root] on dataDir that /data/ serves through. It fails
// only when the embedded UI cannot be loaded.
func baseMux(deps APIDeps, dataRoot *os.Root) (*http.ServeMux, error) {
	myHandler := http.NewServeMux()

	// All URLs will be handled by this function. The read-only routes below
	// are GET (HEAD too), so any other method lands here instead (#149).
	myHandler.HandleFunc("/", handleRequest)
	myHandler.HandleFunc("GET /ignition.json", ignitionHandler(deps.Store, deps.NodeReports, arpLookup, deps.Tickets))
	myHandler.HandleFunc("GET /machineconfig", machineConfigHandler(deps.Store, arpLookup, deps.Tickets))
	myHandler.HandleFunc("GET /preseed", preseedHandler(deps.Store, deps.NodeReports, arpLookup, deps.Tickets))
	myHandler.Handle("GET /booty.ipxe", bootScriptHandler(deps.Store, arpLookup, deps.Tickets))
	myHandler.Handle("GET /menu/", menuSelectionHandler(arpLookup, deps.Tickets))
	// Bare path: every method reaches the handler, so a non-POST is a 405
	// (404 when the feature is off) instead of falling to the "/" catch-all.
	myHandler.Handle("/node/report", nodeReportHandler(deps))
	myHandler.HandleFunc("GET /version.txt", handleVersionRequest)
	myHandler.HandleFunc("GET /version.json", handleVersionRequest)
	myHandler.HandleFunc("GET /healthz", handleHealthz)
	myHandler.HandleFunc("/login", handleLogin(deps.Auth))
	myHandler.HandleFunc("/logout", handleLogout(deps.Auth))
	myHandler.Handle("GET /data/", http.StripPrefix("/data/", dataFileHandler(dataRoot)))
	uiFS, err := web.DistFS()
	if err != nil {
		return nil, fmt.Errorf("http: ui embed: %w", err)
	}
	myHandler.Handle("GET /ui/", http.StripPrefix("/ui/", uiHandler(uiFS)))

	return myHandler, nil
}

// Server is the running HTTP server. Its Shutdown and Close also close the
// [os.Root] that /data/ serves through, as tftp.Server does for TFTP.
type Server struct {
	*http.Server

	dataRoot *os.Root
}

// Shutdown drains the server gracefully (see [http.Server.Shutdown]), then
// closes the /data/ root.
func (s *Server) Shutdown(ctx context.Context) error {
	return errors.Join(s.Server.Shutdown(ctx), s.dataRoot.Close())
}

// Close closes the server immediately (see [http.Server.Close]) and the
// /data/ root.
func (s *Server) Close() error {
	return errors.Join(s.Server.Close(), s.dataRoot.Close())
}

// errNoTicketIssuer is StartHTTP's refusal when APIDeps.Tickets is nil.
var errNoTicketIssuer = errors.New("http: no boot ticket issuer; refusing to start")

// StartHTTP binds the HTTP port, starts serving in a background goroutine and
// returns the server so the caller can Shutdown() it during graceful shutdown.
// Signal handling and the ordered shutdown live with the caller; this function
// only starts serving. A bind failure, an unopenable dataDir or a missing UI
// embed is a startup error; an error after serving starts is sent on errCh,
// as tftp.Start does. ErrServerClosed (a normal shutdown) is not sent.
//
// It refuses to start when deps.Auth is nil and deps.NoAuth is false: that
// combination means no credential was wired at all, and authMiddleware passes
// a nil store through unconditionally (so pre-existing in-process tests keep
// working) -- so this refusal is the only place a silent-open deployment is
// caught. Returning an error rather than calling os.Exit keeps that refusal
// testable.
func StartHTTP(deps APIDeps, errCh chan<- error) (*Server, error) {
	// Checked first and unconditionally, --noAuth included: without an issuer
	// no boot script carries a ticket, so every config route would refuse.
	if deps.Tickets == nil {
		return nil, errNoTicketIssuer
	}
	if deps.Auth == nil && !deps.NoAuth {
		return nil, errors.New("http: no Auth token store and --noAuth not set; refusing to start unauthenticated")
	}

	dataRoot, err := os.OpenRoot(viper.GetString(config.DataDir))
	if err != nil {
		return nil, fmt.Errorf("http: open dataDir: %w", err)
	}

	port := fmt.Sprintf(":%d", viper.GetInt(config.HttpPort))
	slog.Info("starting HTTP server", "addr", port)

	myHandler, err := baseMux(deps, dataRoot)
	if err != nil {
		return nil, errors.Join(err, dataRoot.Close())
	}

	// Mount the typed /api/v1 surface on the same mux (additive).
	RegisterAPI(myHandler, deps)

	// The body timeouts stay long for large artifact downloads. The header and
	// idle bounds stop a LAN client from holding connections open on the open
	// boot routes by trickling headers or parking keep-alives (#122).
	s := &http.Server{
		Addr:              port,
		Handler:           logRequest(withBootRoute(deps.BootFiles, myHandler)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       900 * time.Second,
		WriteTimeout:      900 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", port)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("http: listen on TCP%s: %w", port, err), dataRoot.Close())
	}
	go func() {
		if serveErr := s.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http: %w", serveErr)
		}
	}()
	slog.Info("server started", "addr", ln.Addr().String())

	return &Server{Server: s, dataRoot: dataRoot}, nil
}

// dataFileHandler serves files under dataDir, restricted to the subtrees in
// dataSubtrees (see isAllowedDataPath) and blocking any request whose path
// targets an in-flight staged download (see isPartialPath).
//
// Serving goes through http.FileServer so conditional and ranged GETs keep
// working — iPXE and the multi-GB ISO paths depend on Range — but over a
// listing-free FS, so a directory is indistinguishable from a miss. Files are
// opened through root (an [os.Root] on dataDir) by [dataroot.Open], the same as
// TFTP: it refuses a path or symlink that resolves outside dataDir AND one that
// resolves, inside dataDir, to a path the allowlist does not admit.
func dataFileHandler(root *os.Root) http.Handler {
	dataFS := http.FileServer(noListingFS{root})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Both predicates must judge the SAME path http.FileServer will open,
		// which is path.Clean(r.URL.Path) (net/http/fs.go:995). Testing the raw
		// path let a trailing "/%2e" name an in-flight file while defeating the
		// suffix check — ServeMux cleans the literal "/." spelling away before
		// dispatch, but not the encoded one.
		cleaned := path.Clean("/" + r.URL.Path)
		if !isServedDataPath(cleaned) {
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
// as pkg/tftp's allowedDataFile. Symlinks are not judged here: [dataroot.Open]
// re-runs isServedDataPath on the path a request resolves to, and [os.Root]
// refuses those that leave dataDir.
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

// isServedDataPath reports whether a /data/-relative path may be served: inside
// an allowlisted subtree and not an in-flight download. It is the predicate
// [dataroot.Open] applies to the requested path and to the path it resolves to.
func isServedDataPath(p string) bool {
	return isAllowedDataPath(p) && !isPartialPath(p)
}

// noListingFS is an [http.FileSystem] over an [os.Root] on dataDir with
// directory opens refused, so [http.FileServer] renders no index and cannot
// answer differently for a directory than for a miss. The listing under a
// cached version directory named the in-flight .partial/.download files by
// hand-browsable path, which is the discovery half of fetching one; the
// 301-on-directory behaviour was also an existence oracle for paths the
// allowlist otherwise hides.
type noListingFS struct{ root *os.Root }

// Open opens name, refusing directories and anything [isServedDataPath] does
// not admit, as requested or as resolved through a symlink. Every open failure
// is reported as a miss, as pkg/tftp's openDataFile does: an [os.Root] escape
// error (a symlink leaving dataDir) would otherwise be a 500, which tells a
// prober the path exists.
func (d noListingFS) Open(name string) (http.File, error) {
	// http.FileServer passes a cleaned, slash-prefixed name; dataroot wants it
	// relative to the root.
	f, err := dataroot.Open(d.root, strings.TrimPrefix(name, "/"), isServedDataPath)
	if err != nil {
		return nil, err // already wraps os.ErrNotExist
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

// isPartialPath reports whether a request path targets an in-flight download;
// see cache.IsInFlightPath.
func isPartialPath(p string) bool {
	return cache.IsInFlightPath(p)
}

func logRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request", "remote", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
		handler.ServeHTTP(w, r)
	})
}
