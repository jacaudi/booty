package tftp

import (
	"strings"

	"github.com/jeefy/booty/pkg/cache"
)

// osTitle maps an on-disk cache name to a friendly menu label prefix.
var osTitle = map[string]string{
	"flatcar": "Flatcar",
	"coreos":  "Fedora CoreOS",
	"talos":   "Talos",
}

// menuItemText is the human-readable label for one cache entry, e.g.
// "Talos v1.10.5 (amd64) [schemAAA]". A short schematic prefix disambiguates
// multiple Talos schematics that share a version.
func menuItemText(e cache.CacheEntry) string {
	title := osTitle[e.CacheName]
	if title == "" {
		title = e.CacheName
	}
	label := title + " " + e.Version + " (" + e.Arch + ")"
	if e.Segment != "-" {
		seg := e.Segment
		if len(seg) > 8 {
			seg = seg[:8]
		}
		label += " [" + seg + "]"
	}
	return label
}

// renderMenu builds the iPXE menu script for the cached entries. A leading
// `item retry` is ALWAYS emitted so `choose --default retry` references a real
// label and an empty cache still produces a valid (loop-only) menu. The item KEY
// is the cache-relative path <cacheName>/<segment>/<arch>/<version>, which maps
// directly to the selection-boot filename menu/<key>/boot.ipxe. ASCII only for
// iPXE-build compatibility. serverIP is the bare server IP (for tftp:// chains).
func renderMenu(entries []cache.CacheEntry, serverIP string) string {
	var b strings.Builder
	b.WriteString("#!ipxe\n")
	b.WriteString("menu Booty - select an image to boot\n")
	b.WriteString("item retry Wait / retry\n")
	for _, e := range entries {
		key := e.CacheName + "/" + e.Segment + "/" + e.Arch + "/" + e.Version
		b.WriteString("item " + key + " " + menuItemText(e) + "\n")
	}
	b.WriteString("choose --timeout 60000 --default retry sel || goto retry\n")
	// A "retry" selection isn't a valid 4-segment tuple, so chaining it hits the
	// selection branch's holding fallback (which itself re-chains booty.ipxe) —
	// no special-casing needed, one fewer iPXE command on the unverified surface.
	b.WriteString("chain tftp://" + serverIP + "/menu/${sel}/boot.ipxe || goto retry\n")
	b.WriteString(":retry\n")
	b.WriteString("chain tftp://" + serverIP + "/booty.ipxe || shell\n")
	return b.String()
}
