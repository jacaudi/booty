package http

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/jeefy/booty/pkg/bootscript"
)

// bootScriptHandler serves the per-host booty.ipxe script (holding, assigned
// OS or menu). The host is identified by ?mac=, with ARP as the fallback.
func bootScriptHandler(arp arpFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		script, kind := bootscript.RenderBootScript(identifyHostWith(r, arp), bootscript.URLHost())
		slog.InfoContext(r.Context(), "boot script", "kind", kind, "mac", loggableMAC(r.URL.Query().Get("mac")))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writeBody(w, []byte(script))
	}
}

// menuSelectionHandler serves "menu/<os>/<segment>/<arch>/<version>/boot.ipxe",
// the script for one boot-menu selection, gated on the host being menu-mode.
func menuSelectionHandler(arp arpFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filename := strings.TrimPrefix(r.URL.Path, "/")
		script := bootscript.MenuSelectionScript(identifyHostWith(r, arp), filename, bootscript.URLHost())
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writeBody(w, []byte(script))
	}
}
