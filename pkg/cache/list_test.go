// pkg/cache/list_test.go
package cache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
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

// realTalosHash is a representative Talos schematic hash (not a trivial sentinel)
// that exercises the non-"-" branch of paramSegment. Using a realistic hash
// guards against segment-derivation mismatches between the DB row and the on-disk
// path that would silently mis-group a bootable version in PartitionCached.
const realTalosHash = "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba"

// TestPartitionCachedSchemHash is the primary regression guard for PartitionCached's
// disk↔DB tuple→in_window mapping. It uses a real Talos schematic hash so that
// paramSegment returns the hash (the non-"-" branch), not the trivial "schem1"
// sentinel used elsewhere. A segment-derivation mismatch between the on-disk path
// and the DB lookup key would cause a bootable version to silently mis-group.
func TestPartitionCachedSchemHash(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	store := newReconcileStore(t)

	tid, err := store.CreateTarget(db.Target{
		OS:      "talos",
		Arch:    "amd64",
		Params:  `{"schematic":"` + realTalosHash + `"}`,
		Mode:    "discovery",
		RetainN: 1,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}

	// Seed two versions under the real hash: one in-window, one archived.
	// seedCached now derives the on-disk path from the target's params, so the
	// cache dir is cacheDir("talos", realTalosHash, "amd64", version).
	seedCached(t, store, tid, "v1.13.5", 100, true)  // in-window
	seedCached(t, store, tid, "v1.12.0", 100, false) // archived (in_window=0)

	// Disk-only entry: directory exists but no cache_entries row.
	// PartitionCached must default this to inWindow (safe default — never hides a
	// bootable version, never mis-archives something without an explicit row).
	diskOnlyDir := cacheDir("talos", realTalosHash, "amd64", "v1.11.0")
	if err := os.MkdirAll(diskOnlyDir, 0o755); err != nil {
		t.Fatalf("create disk-only dir: %v", err)
	}

	inWindow, archived := PartitionCached(store)

	inWindowVers := map[string]bool{}
	for _, e := range inWindow {
		inWindowVers[e.Version] = true
	}
	archivedVers := map[string]bool{}
	for _, e := range archived {
		archivedVers[e.Version] = true
	}

	// v1.13.5: in-window row → must appear in inWindow, not archived.
	if !inWindowVers["v1.13.5"] {
		t.Error("v1.13.5 (in_window=1) must appear in inWindow slice")
	}
	if archivedVers["v1.13.5"] {
		t.Error("v1.13.5 (in_window=1) must NOT appear in archived slice")
	}

	// v1.12.0: archived row (in_window=0) → must appear in archived, not inWindow.
	if !archivedVers["v1.12.0"] {
		t.Error("v1.12.0 (in_window=0) must appear in archived slice")
	}
	if inWindowVers["v1.12.0"] {
		t.Error("v1.12.0 (in_window=0) must NOT appear in inWindow slice")
	}

	// v1.11.0: disk-only (no cache_entries row) → safe default is inWindow.
	if !inWindowVers["v1.11.0"] {
		t.Error("v1.11.0 (disk-only, no DB row) must default to inWindow")
	}
	if archivedVers["v1.11.0"] {
		t.Error("v1.11.0 (disk-only, no DB row) must NOT be treated as archived")
	}
}
