package cache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// newEvictFixture returns a Store populated with three cached versions for a
// single talos/amd64/schem1 target:
//
//	v1.10.0  100 B  archived (in_window=0)   — oldest, eviction candidate
//	v1.11.0  100 B  archived (in_window=0)   — second-oldest, eviction candidate
//	v1.13.5  100 B  in-window (in_window=1)  — must never be evicted
//
// Total = 300 B. Seeded oldest-first so fetched_at ASC, id ASC gives a
// deterministic eviction order. This fixture is also reused by Task 4's scan
// test in the same package.
func newEvictFixture(t *testing.T) *db.Store {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	store := newReconcileStore(t)
	tid, err := store.CreateTarget(db.Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"schem1"}`, Mode: "discovery", RetainN: 1, Enabled: true})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	seedCached(t, store, tid, "v1.10.0", 100, false) // oldest, archived
	seedCached(t, store, tid, "v1.11.0", 100, false) // archived
	seedCached(t, store, tid, "v1.13.5", 100, true)  // in-window
	return store
}

// seedCached creates a target_versions row, the on-disk directory with a
// kernel-<arch> file of the given size, a cache_entries row (UpsertCacheEntry),
// and optionally archives it (SetCacheInWindow false). The on-disk path is
// derived from the target's own OS/Arch/Params so the disk layout always matches
// what ListCached() discovers — no hardcoded segment. This helper is reused by
// the scan and partition tests in the same package — keep the interface general.
func seedCached(t *testing.T, store *db.Store, tid int64, version string, size int64, inWindow bool) {
	t.Helper()
	tgt, err := store.GetTarget(tid)
	if err != nil {
		t.Fatalf("seedCached GetTarget(%d): %v", tid, err)
	}
	params, err := decodeParams(tgt.Params)
	if err != nil {
		t.Fatalf("seedCached decodeParams: %v", err)
	}
	cacheName := canonicalToCacheName(tgt.OS)
	segment := paramSegment(params)
	if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: tid, Version: version, Source: "discovered", Cached: true}); err != nil {
		t.Fatal(err)
	}
	dir := cacheDir(cacheName, segment, tgt.Arch, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kernel-"+tgt.Arch), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	tvID, err := store.TargetVersionID(tid, version)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCacheEntry(tvID, size); err != nil {
		t.Fatal(err)
	}
	if !inWindow {
		if err := store.SetCacheInWindow(tvID, false); err != nil {
			t.Fatal(err)
		}
	}
}

// TestEvictRemovesOldestArchivedUnpinnedOverBudget: budget=150, total=300.
// Evicts v1.10.0 (oldest archived) then v1.11.0; v1.13.5 (in-window) untouched.
func TestEvictRemovesOldestArchivedUnpinnedOverBudget(t *testing.T) {
	store := newEvictFixture(t)
	if err := evictOverBudget(store, 150); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	rows, _ := store.ListCacheEntries(db.CacheFilter{})
	for _, r := range rows {
		got[r.Version] = true
	}
	if got["v1.10.0"] {
		t.Fatal("v1.10.0 (oldest archived) must be evicted first")
	}
	if got["v1.11.0"] {
		t.Fatal("v1.11.0 must also be evicted to reach budget")
	}
	if !got["v1.13.5"] {
		t.Fatal("v1.13.5 (in-window) must never be evicted")
	}
	if cacheDirExists("talos", "schem1", "amd64", "v1.10.0") {
		t.Fatal("evicted version's dir must be removed")
	}
}

// TestEvictUnlimitedIsNoOp: maxBytes=0 means unlimited; nothing evicted.
func TestEvictUnlimitedIsNoOp(t *testing.T) {
	store := newEvictFixture(t)
	before, _ := store.ListCacheEntries(db.CacheFilter{})
	if err := evictOverBudget(store, 0); err != nil {
		t.Fatal(err)
	}
	after, _ := store.ListCacheEntries(db.CacheFilter{})
	if len(after) != len(before) {
		t.Fatal("maxBytes=0 must not evict anything")
	}
}

// TestEvictNeverTouchesPinned: with v1.10.0 pinned, heavy budget pressure (50 B)
// must evict v1.11.0 but leave the pinned v1.10.0 and in-window v1.13.5 intact.
func TestEvictNeverTouchesPinned(t *testing.T) {
	store := newEvictFixture(t)
	rows, _ := store.ListCacheEntries(db.CacheFilter{})
	for _, r := range rows {
		if r.Version == "v1.10.0" {
			_ = store.SetCachePinned(r.ID, true) // pin the oldest archived
		}
	}
	if err := evictOverBudget(store, 50); err != nil { // heavy pressure, but pinned + in-window are protected
		t.Fatal(err)
	}
	rows, _ = store.ListCacheEntries(db.CacheFilter{})
	found := false
	for _, r := range rows {
		if r.Version == "v1.10.0" {
			found = true
		}
	}
	if !found {
		t.Fatal("pinned archived version must never be evicted")
	}
}

// TestEvictNeverRemovesNewestCachedVersion (D13): with two archived flatcar
// versions on disk and a byte budget below their total, eviction reclaims the
// OLDER one but must leave the NEWEST cached version — the bytes NewestCached
// serves — even though it is archived, so the boot fallback never 404s.
func TestEvictNeverRemovesNewestCachedVersion(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	store := newReconcileStore(t)

	tid, err := store.CreateTarget(db.Target{OS: "flatcar", Arch: "amd64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	seed := func(version string) int64 {
		if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: tid, Version: version, Source: "discovered", Cached: true}); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(cacheDir("flatcar", "stable", "amd64", version), 0o755); err != nil {
			t.Fatal(err)
		}
		tvID, _ := store.TargetVersionID(tid, version)
		if err := store.UpsertCacheEntry(tvID, 1000); err != nil {
			t.Fatal(err)
		}
		if err := store.SetCacheInWindow(tvID, false); err != nil { // both archived
			t.Fatal(err)
		}
		return tvID
	}
	seed("100.0.0")
	seed("100.1.0") // newest by CompareVersions

	if err := evictOverBudget(store, 1500); err != nil { // budget below 2000 total
		t.Fatalf("evict: %v", err)
	}
	if cacheDirExists("flatcar", "stable", "amd64", "100.0.0") {
		t.Error("older archived version should have been evicted")
	}
	if !cacheDirExists("flatcar", "stable", "amd64", "100.1.0") {
		t.Error("newest cached version must NEVER be evicted (D13) — boot fallback would 404")
	}
}
