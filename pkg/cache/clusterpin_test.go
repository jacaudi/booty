package cache

import (
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// TestEvictNeverEvictsClusterReferencedVersion (M3): an ARCHIVED, OLD talos
// version that is neither newest nor in-window — normally prime eviction bait —
// must survive when a live cluster pins its (schematic, version). Without the
// M3 guard, D13 would NOT protect it (it is not the newest cached), so this
// fixture proves the new retention input, not a re-test of D13.
func TestEvictNeverEvictsClusterReferencedVersion(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	viper.Set(config.TalosSchematic, "defaultschematic")
	viper.Set(config.TalosArchitecture, "amd64")
	store := newReconcileStore(t)

	tid, err := store.CreateTarget(db.Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"schemP"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// Two archived versions; a NEWER cached in-window one so D13 protects the
	// newest, NOT the pinned old one.
	seedCached(t, store, tid, "v1.11.0", 100, false) // OLD, archived, pinned by cluster
	seedCached(t, store, tid, "v1.13.9", 100, true)  // newest, in-window (D13-protected)

	// A live cluster pins (schemP, v1.11.0).
	cid, err := store.CreateCluster("pinner", "https://e:6443", "v1.11.0", "v1.34.0", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertHost(db.Host{MAC: "aa:bb:cc:dd:ee:b0", OS: "talos", Schematic: "schemP"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHostCluster("aa:bb:cc:dd:ee:b0", &cid); err != nil {
		t.Fatal(err)
	}

	// Budget below total forces eviction; the only unprotected candidate is the
	// pinned v1.11.0 — the M3 guard must skip it, leaving nothing to evict.
	if err := evictOverBudget(store, 150); err != nil {
		t.Fatalf("evict: %v", err)
	}
	if !cacheDirExists("talos", "schemP", "amd64", "v1.11.0") {
		t.Fatal("cluster-referenced version must never be evicted (M3)")
	}
	rows, _ := store.ListCacheEntries(db.CacheFilter{})
	found := false
	for _, r := range rows {
		if r.Version == "v1.11.0" {
			found = true
		}
	}
	if !found {
		t.Fatal("cluster-referenced version's row must survive (M3)")
	}
}
