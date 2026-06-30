// pkg/tftp/menu_test.go
package tftp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func TestRenderMenuItemsAndRetry(t *testing.T) {
	entries := []cache.CacheEntry{
		{CacheName: "flatcar", Segment: "-", Arch: "amd64", Version: "3815.2.0"},
		{CacheName: "talos", Segment: "schemAAAAAAAA", Arch: "amd64", Version: "v1.10.5"},
	}
	got := renderMenu(entries, "10.0.0.1")

	if !strings.HasPrefix(got, "#!ipxe\n") {
		t.Errorf("missing shebang: %q", got)
	}
	// always-present retry item (default target + empty-cache safety)
	if !strings.Contains(got, "item retry ") {
		t.Errorf("missing retry item:\n%s", got)
	}
	// item key is the cache-relative path
	if !strings.Contains(got, "item flatcar/-/amd64/3815.2.0 ") {
		t.Errorf("missing flatcar item key:\n%s", got)
	}
	if !strings.Contains(got, "item talos/schemAAAAAAAA/amd64/v1.10.5 ") {
		t.Errorf("missing talos item key:\n%s", got)
	}
	// choose line governs default + timeout (spec-required)
	if !strings.Contains(got, "choose --timeout 60000 --default retry sel || goto retry") {
		t.Errorf("missing or wrong choose line:\n%s", got)
	}
	// selection chains to the synthetic menu path
	if !strings.Contains(got, "chain tftp://10.0.0.1/menu/${sel}/boot.ipxe") {
		t.Errorf("missing selection chain:\n%s", got)
	}
	// retry re-chains booty.ipxe over TFTP
	if !strings.Contains(got, "chain tftp://10.0.0.1/booty.ipxe") {
		t.Errorf("missing retry re-chain:\n%s", got)
	}
	// full-range ASCII guard — iPXE-build compatibility requires ASCII-only output
	for i, r := range got {
		if r > 127 {
			t.Errorf("non-ASCII rune %q at byte %d; menu must be ASCII-only:\n%s", r, i, got)
			break
		}
	}
}

func TestRenderMenuEmptyCacheIsLoopOnly(t *testing.T) {
	got := renderMenu(nil, "10.0.0.1")
	if !strings.Contains(got, "item retry ") {
		t.Errorf("empty menu must still have retry item:\n%s", got)
	}
	if strings.Count(got, "item ") != 1 {
		t.Errorf("empty menu must have exactly one item (retry):\n%s", got)
	}
}

func TestRenderMenuSelectionValid(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	viper.Set(config.CoreOSChannel, "stable")
	if err := os.MkdirAll(filepath.Join(root, "cache", "talos", "schemA", "amd64", "v1.10.5"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := renderMenuSelection("menu/talos/schemA/amd64/v1.10.5/boot.ipxe", "10.0.0.1")
	if err != nil {
		t.Fatalf("valid selection errored: %v", err)
	}
	// rendered the talos template for the EXACT version (baseurl carries it)
	if !strings.Contains(got, cache.CacheURLBase("10.0.0.1", "talos", "schemA", "amd64", "v1.10.5")) {
		t.Errorf("selection did not render exact tuple:\n%s", got)
	}
}

// TestMenuSelectionScript covers the approval-gate gate added to the
// menu-selection TFTP branch (I1). Three behaviours must hold:
//   - a non-menu-mode host gets the holding fallback regardless of tuple validity
//   - a menu-mode host with a valid tuple gets the OS boot script
//   - a menu-mode host with an invalid/uncached tuple still gets the holding fallback
//
// This restores symmetry with the booty.ipxe branch's bootDispatch gate.
func TestMenuSelectionScript(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	viper.Set(config.ServerIP, "10.0.0.1")
	if err := os.MkdirAll(filepath.Join(root, "cache", "talos", "schemA", "amd64", "v1.10.5"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const (
		server      = "10.0.0.1"
		validFile   = "menu/talos/schemA/amd64/v1.10.5/boot.ipxe"
		invalidFile = "menu/talos/schemA/amd64/v9.9.9/boot.ipxe" // not cached
	)
	holdingMarker := "tftp://" + server + "/booty.ipxe"
	validMarker := cache.CacheURLBase(server, "talos", "schemA", "amd64", "v1.10.5")

	menuHost := &hardware.Host{Approved: true, BootMode: "menu"}

	cases := []struct {
		name        string
		host        *hardware.Host
		filename    string
		wantHolding bool
	}{
		{"nil host → holding", nil, validFile, true},
		{"approved assigned host → holding (not menu mode)", &hardware.Host{Approved: true, BootMode: "assigned", OS: "talos"}, validFile, true},
		{"menu host + valid tuple → OS boot script", menuHost, validFile, false},
		{"menu host + invalid tuple → holding", menuHost, invalidFile, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := menuSelectionScript(tc.host, tc.filename, server)
			if tc.wantHolding {
				if !strings.Contains(got, holdingMarker) {
					t.Errorf("expected holding script (re-chains %q), got:\n%s", holdingMarker, got)
				}
				if strings.Contains(got, validMarker) {
					t.Errorf("holding response must not contain OS boot URL %q:\n%s", validMarker, got)
				}
			} else {
				if !strings.Contains(got, validMarker) {
					t.Errorf("expected OS boot script (contains %q), got:\n%s", validMarker, got)
				}
			}
		})
	}
}

func TestRenderMenuSelectionRejects(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	if err := os.MkdirAll(filepath.Join(root, "cache", "talos", "schemA", "amd64", "v1.10.5"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	bad := []string{
		"menu/talos/schemA/amd64/boot.ipxe",               // 3 segments
		"menu/talos/schemA/amd64/v1.10.5/extra/boot.ipxe", // 5 segments
		"menu/bogusos/-/amd64/v1.0.0/boot.ipxe",           // unknown os
		"menu/talos/schemA/amd64/v9.9.9/boot.ipxe",        // not cached
		"menu/talos/schemA/amd64/not-a-version/boot.ipxe",  // invalid version
		"menu/talos/../amd64/v1.10.5/boot.ipxe",            // traversal
	}
	for _, f := range bad {
		if _, err := renderMenuSelection(f, "10.0.0.1"); err == nil {
			t.Errorf("expected rejection for %q", f)
		}
	}
}
