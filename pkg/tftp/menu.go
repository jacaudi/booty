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
	"flatcar":       "Flatcar",
	"coreos":        "Fedora CoreOS",
	"talos":         "Talos",
	"debian":        "Debian",
	"systemrescue":  "SystemRescue",
	"uefi-shell":    "UEFI Shell",
	"memtest86plus": "Memtest86+",
	"clonezilla":    "Clonezilla",
	"rescatux":      "Rescatux",
	"zfsbootmenu":   "ZFSBootMenu",
	"shredos":       "ShredOS (disk eraser)",
	"tails":         "Tails",
}

// itemKey is the single source of the menu item-key format: the cache-relative
// path <cacheName>/<segment>/<arch>/<version>. It is used verbatim as the
// iPXE `item` key in every menu block (main, archived, tools), and must stay
// in lockstep with the selection-boot path (menu/<key>/boot.ipxe) that
// renderMenuSelection parses back into its four segments — a drift here
// breaks every menu selection silently.
func itemKey(e cache.CacheEntry) string {
	return e.CacheName + "/" + e.Segment + "/" + e.Arch + "/" + e.Version
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

// renderMenu builds the iPXE menu script for the partitioned cache entries.
// inWindow entries appear in the main menu; archived entries (in_window=0) are
// grouped under a nested "Archived OSes" sub-menu reachable from the main block.
// A leading `item retry` is ALWAYS emitted so `choose --default retry` has a
// real target and an empty cache still produces a valid (loop-only) menu.
// The item KEY is the cache-relative path <cacheName>/<segment>/<arch>/<version>,
// which maps directly to the selection-boot path menu/<key>/boot.ipxe.
// ASCII only for iPXE-build compatibility. serverIP is the bare server IP.
// The invariant is the guarded iseq/goto dispatch shape: nav sentinels use
// goto-label, boot tuples fall through to the chain command.
func renderMenu(inWindow, archived []cache.CacheEntry, serverIP string) string {
	var osEntries, toolEntries []cache.CacheEntry
	for _, e := range inWindow {
		if isToolOS(e.CacheName) {
			toolEntries = append(toolEntries, e)
		} else {
			osEntries = append(osEntries, e)
		}
	}

	var b strings.Builder
	b.WriteString("#!ipxe\n")
	b.WriteString(":top\n")
	b.WriteString("menu Booty - select an image to boot\n")
	b.WriteString("item retry Wait / retry\n")
	for _, e := range osEntries {
		b.WriteString("item " + itemKey(e) + " " + menuItemText(e) + "\n")
	}
	if len(toolEntries) > 0 {
		b.WriteString("item tools Tools & rescue...\n")
	}
	if len(archived) > 0 {
		b.WriteString("item archived Archived OSes...\n")
	}
	b.WriteString("choose --timeout 300000 --default retry sel || goto retry\n")
	// One guarded line per sentinel with an explicit fall-through label. The
	// `|| goto` is MANDATORY, not stylistic: iPXE aborts a script on the first
	// failing command and iseq fails on mismatch, so a bare
	// `iseq ${sel} tools && goto tools` would kill the menu on every other
	// selection.
	switch {
	case len(toolEntries) > 0 && len(archived) > 0:
		b.WriteString("iseq ${sel} tools && goto tools || goto nottools\n")
		b.WriteString(":nottools\n")
		b.WriteString("iseq ${sel} archived && goto archived || goto boot\n")
	case len(toolEntries) > 0:
		b.WriteString("iseq ${sel} tools && goto tools || goto boot\n")
	case len(archived) > 0:
		b.WriteString("iseq ${sel} archived && goto archived || goto boot\n")
	default:
		b.WriteString("goto boot\n")
	}
	b.WriteString(":boot\n")
	b.WriteString("chain tftp://" + serverIP + "/menu/${sel}/boot.ipxe || goto retry\n")

	if len(archived) > 0 {
		b.WriteString(":archived\n")
		b.WriteString("menu Booty - Archived OSes\n")
		b.WriteString("item back Back\n")
		for _, e := range archived {
			b.WriteString("item " + itemKey(e) + " " + menuItemText(e) + "\n")
		}
		b.WriteString("choose --timeout 300000 --default back asel || goto top\n")
		b.WriteString("iseq ${asel} back && goto top || goto bootarchived\n")
		b.WriteString(":bootarchived\n")
		b.WriteString("chain tftp://" + serverIP + "/menu/${asel}/boot.ipxe || goto top\n")
	}

	if len(toolEntries) > 0 {
		b.WriteString(":tools\n")
		b.WriteString("menu Booty - Tools & rescue\n")
		b.WriteString("item back Back\n")
		for _, e := range toolEntries {
			b.WriteString("item " + itemKey(e) + " " + menuItemText(e) + "\n")
		}
		// tsel: each menu needs its own choose variable (sel and asel are taken).
		b.WriteString("choose --timeout 300000 --default back tsel || goto top\n")
		b.WriteString("iseq ${tsel} back && goto top || goto boottools\n")
		b.WriteString(":boottools\n")
		b.WriteString("chain tftp://" + serverIP + "/menu/${tsel}/boot.ipxe || goto top\n")
	}

	b.WriteString(":retry\n")
	b.WriteString("chain tftp://" + serverIP + "/booty.ipxe || shell\n")
	return b.String()
}
