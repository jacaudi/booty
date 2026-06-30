package tftp

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
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

// renderMenuSelection parses a synthetic menu-selection filename
// "menu/<cacheName>/<segment>/<arch>/<version>/boot.ipxe", validates it against
// the on-disk cache, and renders that OS's iPXE template for the EXACT tuple via
// bootTokensFor. It returns an error for any malformed/unknown/missing/invalid or
// traversal selection so the caller serves the holding fallback instead —
// arbitrary disk content is never served. The path is rebuilt from a fixed
// 4-segment split (cache.ValidCachedSelection), so a segment cannot smuggle traversal.
func renderMenuSelection(filename, urlHost string) (string, error) {
	inner := strings.TrimSuffix(strings.TrimPrefix(filename, "menu/"), "/boot.ipxe")
	parts := strings.Split(inner, "/")
	if len(parts) != 4 {
		return "", fmt.Errorf("tftp: menu selection %q: want 4 segments, got %d", filename, len(parts))
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", errPathEscapes
		}
	}
	cacheName, segment, arch, version := parts[0], parts[1], parts[2], parts[3]
	if !cache.ValidCachedSelection(cacheName, segment, arch, version) {
		return "", os.ErrNotExist
	}
	tmpl, ok := PXEConfig[cacheName+".ipxe"]
	if !ok {
		return "", fmt.Errorf("tftp: menu selection: no template for %q", cacheName)
	}
	return applyTokens(tmpl, bootTokensFor(cacheName, segment, arch, version, urlHost)), nil
}

// menuSelectionScript returns the iPXE script to serve for a TFTP
// "menu/<tuple>/boot.ipxe" request. It gates on host state via bootDispatch,
// mirroring the gate that the booty.ipxe branch already applies — only a host
// that bootDispatch classifies as "menu" (approved + boot_mode='menu') may boot
// a selection. Any other host state (unapproved, holding, assigned) receives the
// holding fallback regardless of tuple validity. This closes the approval-gate
// asymmetry between the two TFTP branches.
//
// For menu-mode hosts, renderMenuSelection validates the tuple and renders the
// OS template; on any validation error the holding fallback is still served
// (behaviour unchanged from before this gate was added).
func menuSelectionScript(host *hardware.Host, filename, urlHost string) string {
	holding := applyTokens(PXEConfig["holding.ipxe"], map[string]string{
		"[[server]]":    urlHost,
		"[[server-ip]]": viper.GetString(config.ServerIP),
	})
	// Gate: restore symmetry with the booty.ipxe branch — only menu-mode hosts
	// may boot a selection.
	kind, _ := bootDispatch(host)
	if kind != "menu" {
		return holding
	}
	toServe, err := renderMenuSelection(filename, urlHost)
	if err != nil {
		slog.Warn("TFTP: menu selection rejected, serving holding", "file", filename, "err", err)
		return holding
	}
	return toServe
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
