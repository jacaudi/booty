// pkg/tftp/menu_test.go
package tftp

import (
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cache"
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
