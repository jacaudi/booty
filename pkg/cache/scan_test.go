package cache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

func TestScanRepairsAndReportsOrphans(t *testing.T) {
	store := newEvictFixture(t) // 3 cached versions with rows + dirs
	tgts, _ := store.ListTargets()
	tid := tgts[0].ID

	// 4th cached version: target_version + dir, but NO cache_entries row (missing detail).
	_ = store.UpsertTargetVersion(db.TargetVersion{TargetID: tid, Version: "v1.9.0", Source: "discovered", Cached: true})
	dir := cacheDir("talos", "schem1", "amd64", "v1.9.0")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "kernel-amd64"), make([]byte, 42), 0o644)

	// Orphan dir on disk with no target_version.
	_ = os.MkdirAll(cacheDir("talos", "schem1", "amd64", "v9.9.9"), 0o755)

	before, _ := store.ListCacheEntries(db.CacheFilter{})
	res, err := Scan(store)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := store.ListCacheEntries(db.CacheFilter{})
	if len(after) != len(before)+1 { // repaired the missing v1.9.0 row
		t.Fatalf("scan should repair the missing row: before %d, after %d", len(before), len(after))
	}
	if res.Orphans < 1 {
		t.Fatalf("scan should report the orphan dir, got %d", res.Orphans)
	}
}

// TestScanSkipsPartialFiles asserts an in-flight .partial staged download is
// never counted toward a cached version's size — a .partial is unverified
// in-flight bytes, not a cached artifact (T2/T9 stage artifacts as
// <file>.partial before landing them).
func TestScanSkipsPartialFiles(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	store := newReconcileStore(t)
	tid, _ := store.CreateTarget(db.Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"s"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
	store.UpsertTargetVersion(db.TargetVersion{TargetID: tid, Version: "v1.0.0", Source: "discovered", Cached: true})
	dir := cacheDir("talos", "s", "amd64", "v1.0.0")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "kernel-amd64"), []byte("12345"), 0o644)                // 5 bytes
	os.WriteFile(filepath.Join(dir, "initramfs-amd64.xz.partial"), []byte("999999"), 0o644) // in-flight, must be ignored

	if _, err := Scan(store); err != nil {
		t.Fatal(err)
	}
	rows, _ := store.ListCacheEntries(db.CacheFilter{})
	if len(rows) != 1 || rows[0].Size != 5 {
		t.Fatalf("Scan must exclude *.partial from size, got %+v", rows)
	}
}
