// Package bootscript renders the iPXE boot scripts and menus that booty
// serves to PXE clients, and the per-boot tokens they carry.
package bootscript

import (
	"fmt"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

// defaultHTTPPort is the HTTP port that URLHost omits from the host string.
const defaultHTTPPort = 80

// Boot kinds reported by bootDispatch and RenderBootScript.
const (
	kindHolding  = "holding"
	kindAssigned = "assigned"
	kindMenu     = "menu"
)

// URLHost returns the host[:port] that boot scripts use to reach booty over
// HTTP: the server IP, plus the HTTP port unless it is 80.
func URLHost() string {
	urlHost := viper.GetString(config.ServerIP)
	if hostPort := viper.GetInt(config.ServerHttpPort); hostPort != defaultHTTPPort {
		urlHost = fmt.Sprintf("%s:%d", urlHost, hostPort)
	}
	return urlHost
}

// StubScript is the whole of booty.ipxe over TFTP: it hands the client to the
// HTTP renderer, passing the MAC iPXE reports so no ARP is needed.
func StubScript(urlHost string) string {
	return "#!ipxe\nchain http://" + urlHost + ChainPath + " || shell\n"
}

// RenderBootScript renders the per-host booty.ipxe script and returns it with
// the kind of boot (holding, assigned, menu) it chose. issuer mints the boot
// ticket an assigned OS script carries; the holding and menu scripts carry none.
func RenderBootScript(host *hardware.Host, urlHost string, issuer TicketIssuer) (script, kind string) {
	kind, _ = bootDispatch(host)
	switch kind {
	case kindAssigned:
		return assignedScript(host, urlHost, issuer), kind
	case kindMenu:
		var inWindow, archEntries []cache.CacheEntry
		if s := currentStore(); s != nil {
			inWindow, archEntries = cache.PartitionCached(s)
		} else {
			inWindow = cache.ListCached() // no store: everything in-window (safe default)
		}
		return renderMenu(inWindow, archEntries, urlHost), kind
	default: // holding
		return holdingScript(urlHost), kind
	}
}
