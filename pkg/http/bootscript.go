package http

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/bootscript"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/nodereport"
)

// bootScriptHandler serves the per-host booty.ipxe script (holding, assigned
// OS or menu). The host is identified by ?mac=, with ARP as the fallback. A GET
// from a MAC that is not registered is also recorded in discovered_hosts when
// store is non-nil; recording never changes the script served.
func bootScriptHandler(store *db.Store, arp arpFunc) http.HandlerFunc {
	var d *discoverer
	if store != nil {
		d = &discoverer{
			store: store,
			limit: nodereport.MustNewLimiter(discoveryPerIP, discoveryWindow, discoveryLimiterCapacity, time.Now),
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		host, mac, err := resolveBootHost(r, arp)
		if d != nil && r.Method == http.MethodGet && host == nil && mac != "" && errors.Is(err, hardware.ErrNotFound) {
			d.record(r, mac)
		}
		script, kind := bootscript.RenderBootScript(host, bootscript.URLHost())
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
