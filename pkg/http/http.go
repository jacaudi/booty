package http

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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

// dataFileHandler serves files under dataDir, blocking any request whose
// (decoded) path targets an in-flight staged download (see isPartialPath).
// Extracted from StartHTTP's /data/ registration so it is independently
// testable via httptest without standing up the full mux/server.
func dataFileHandler(dataDir string) http.Handler {
	dataFS := http.FileServer(http.Dir(dataDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPartialPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		dataFS.ServeHTTP(w, r)
	})
}

// isPartialPath reports whether a request path targets an in-flight staged
// download. Such files must never be served (they are incomplete/unverified);
// the boot path never references them, this guards direct /data/ browsing.
// Case-insensitive: a .partial file is always written lowercase, but on a
// case-insensitive dev filesystem (e.g. macOS/APFS) a request for
// "kernel.PARTIAL" would otherwise resolve to the same on-disk file and
// bypass a case-sensitive check.
func isPartialPath(p string) bool { return strings.HasSuffix(strings.ToLower(p), ".partial") }

func logRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request", "remote", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
		handler.ServeHTTP(w, r)
	})
}
