// pkg/cache/list_test.go
package cache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func seedVer(t *testing.T, root, cacheName, seg, arch, ver string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "cache", cacheName, seg, arch, ver), 0o755); err != nil {
		t.Fatalf("seed %s/%s/%s/%s: %v", cacheName, seg, arch, ver, err)
	}
}

func TestListCached(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)

	// valid entries across three OSes (talos & flatcar = semver; fcos = dotted-numeric)
	seedVer(t, root, "flatcar", "-", "amd64", "3815.2.0")
	seedVer(t, root, "talos", "schemA", "amd64", "v1.10.5")
	seedVer(t, root, "talos", "schemA", "amd64", "v1.10.4") // older, same line
	seedVer(t, root, "coreos", "-", "x86_64", "40.20240101.3.0")
	// noise that must be skipped:
	seedVer(t, root, "talos", "schemA", "amd64", "not-a-version") // fails ValidateVersion
	seedVer(t, root, "bogusos", "-", "amd64", "v1.0.0")           // unknown cache name

	got := ListCached()

	// expect exactly 4 valid entries
	if len(got) != 4 {
		t.Fatalf("ListCached returned %d entries, want 4: %+v", len(got), got)
	}
	// no invalid version, no unknown OS
	for _, e := range got {
		if e.Version == "not-a-version" || e.CacheName == "bogusos" {
			t.Errorf("unexpected entry surfaced: %+v", e)
		}
	}
	// within talos/schemA, newest version sorts first
	var talos []CacheEntry
	for _, e := range got {
		if e.CacheName == "talos" {
			talos = append(talos, e)
		}
	}
	if len(talos) != 2 || talos[0].Version != "v1.10.5" {
		t.Fatalf("talos ordering wrong (want v1.10.5 first): %+v", talos)
	}
}

func TestValidCachedSelection(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	seedVer(t, root, "talos", "schemA", "amd64", "v1.10.5")

	if !ValidCachedSelection("talos", "schemA", "amd64", "v1.10.5") {
		t.Error("valid talos selection rejected")
	}
	if ValidCachedSelection("talos", "schemA", "amd64", "v9.9.9") {
		t.Error("non-existent version accepted")
	}
	if ValidCachedSelection("bogusos", "-", "amd64", "v1.0.0") {
		t.Error("unknown OS accepted")
	}
	if ValidCachedSelection("talos", "schemA", "amd64", "not-a-version") {
		t.Error("invalid version string accepted")
	}
	// Seed the path that filepath.Join(root,"cache","talos","..","amd64","v1.10.5")
	// collapses to, proving the traversal test catches a real directory hit rather
	// than passing coincidentally on non-existence.
	if err := os.MkdirAll(filepath.Join(root, "cache", "amd64", "v1.10.5"), 0o755); err != nil {
		t.Fatalf("seed traversal dir: %v", err)
	}
	if ValidCachedSelection("talos", "..", "amd64", "v1.10.5") {
		t.Error("traversal segment accepted")
	}
}
