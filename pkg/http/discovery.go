package http

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/jeefy/booty/pkg/bootscript"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/nodereport"
)

const (
	// maxDiscoveredHosts caps the discovered_hosts table. It never evicts: a
	// full table refuses new rows, so a flood cannot push out a real machine.
	maxDiscoveredHosts = 1024
	// discoveryPerIP is the new rows one source key may add per discoveryWindow.
	discoveryPerIP  = 8
	discoveryWindow = 10 * time.Minute
	// discoveryLimiterCapacity bounds how many source keys the limiter keeps.
	discoveryLimiterCapacity = 4096
)

// discoveredArch maps the iPXE ${buildarch} value to a booty arch. Anything
// else, including an absent value, is "".
func discoveredArch(raw string) string {
	switch raw {
	case "x86_64", "i386":
		return "amd64"
	case "arm64":
		return "arm64"
	default:
		return ""
	}
}

// discoverySource returns the limiter key and the address to store for a
// request's RemoteAddr. An IPv4 source (or v4-mapped IPv6) keys on its address;
// an IPv6 source keys on its /64, so one subnet cannot mint a key per packet.
// An unparseable address keys on itself.
func discoverySource(remoteAddr string) (key, ip string) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return host, host
	}
	a = a.Unmap().WithZone("")
	ip = a.String()
	if a.Is4() {
		return ip, ip
	}
	return netip.PrefixFrom(a, 64).Masked().String(), ip
}

// discoverer records machines that fetch booty.ipxe without being registered.
// The route is open and the table it writes is capped, so inserts are rate
// limited per source; a refresh of an existing row costs no budget.
type discoverer struct {
	store *db.Store
	limit *nodereport.Limiter
}

// record files mac as discovered, or refreshes its row. Failures and refusals
// are logged and swallowed: recording must never change the script served.
func (d *discoverer) record(r *http.Request, mac string) {
	ctx := r.Context()
	arch := discoveredArch(r.URL.Query().Get(bootscript.ArchQueryKey))
	key, ip := discoverySource(r.RemoteAddr)

	exists, err := d.store.TouchDiscoveredHost(ctx, mac, ip, arch)
	if err != nil {
		slog.WarnContext(ctx, "discovery touch failed", "mac", mac, "err", err)
		return
	}
	if exists {
		return
	}
	if !d.limit.Allow(key) {
		slog.DebugContext(ctx, "discovery refused: rate limited", "mac", mac)
		return
	}
	added, err := d.store.InsertDiscoveredHost(ctx, mac, ip, arch, maxDiscoveredHosts)
	if err != nil {
		slog.WarnContext(ctx, "discovery insert failed", "mac", mac, "err", err)
		return
	}
	if !added {
		slog.DebugContext(ctx, "discovery refused: table full or registered", "mac", mac)
	}
}
