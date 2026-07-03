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
	myHandler.HandleFunc("/ignition.json", handleIgnitionRequest)
	myHandler.HandleFunc("/machineconfig", handleMachineConfigRequest)
	myHandler.HandleFunc("/version.txt", handleVersionRequest)
	myHandler.HandleFunc("/version.json", handleVersionRequest)
	myHandler.HandleFunc("/hosts", handleHostsRequest)
	myHandler.HandleFunc("/register", handleRegistrationRequest)
	myHandler.HandleFunc("/unregister", handleUnregistrationRequest)
	myHandler.HandleFunc("/booty.json", handleDataRequest)
	myHandler.HandleFunc("/info", handleInfoRequest)
	dataFS := http.FileServer(http.Dir(viper.GetString(config.DataDir)))
	myHandler.Handle("/data/", http.StripPrefix("/data/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPartialPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		dataFS.ServeHTTP(w, r)
	})))
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

// isPartialPath reports whether a request path targets an in-flight staged
// download. Such files must never be served (they are incomplete/unverified);
// the boot path never references them, this guards direct /data/ browsing.
func isPartialPath(p string) bool { return strings.HasSuffix(p, ".partial") }

func logRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request", "remote", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
		handler.ServeHTTP(w, r)
	})
}
