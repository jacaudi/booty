package http

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/tftp"
)

// bootScriptHandler serves the per-host booty.ipxe script (holding, assigned
// OS or menu). The host is identified by ?mac=, with ARP as the fallback.
func bootScriptHandler(arp arpFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		script, kind := tftp.RenderBootScript(identifyHostWith(r, arp), tftp.URLHost())
		// Log the normalised MAC ("" when ?mac= does not parse), never the
		// raw, attacker-controlled query value.
		mac, _ := hardware.NormalizeMAC(r.URL.Query().Get("mac"))
		//nolint:sloglint // default logger: no injected logger in this package yet
		slog.Info("boot script", "kind", kind, "mac", mac)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, script)
	}
}

// menuSelectionHandler serves "menu/<os>/<segment>/<arch>/<version>/boot.ipxe",
// the script for one boot-menu selection, gated on the host being menu-mode.
func menuSelectionHandler(arp arpFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filename := strings.TrimPrefix(r.URL.Path, "/")
		script := tftp.MenuSelectionScript(identifyHostWith(r, arp), filename, tftp.URLHost())
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		//nolint:gosec // G705: text/plain iPXE script, not HTML
		_, _ = io.WriteString(w, script)
	}
}
