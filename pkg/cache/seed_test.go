package cache

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

func seedTestStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.Open(filepath.Join(t.TempDir(), "booty.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func targetByOSParams(t *testing.T, store *db.Store, os, params string) (db.Target, bool) {
	t.Helper()
	all, err := store.ListTargets()
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	for _, tg := range all {
		if tg.OS == os && tg.Params == params {
			return tg, true
		}
	}
	return db.Target{}, false
}

func TestSeedTargets_PredefinedSet(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosSchematic, "defschem")
	viper.Set(config.TalosRetainMinors, 3)

	store := seedTestStore(t) // empty host store: zero registered hosts

	if err := seedTargets(store); err != nil {
		t.Fatalf("seedTargets: %v", err)
	}
	// Idempotent: a second seed must not duplicate.
	if err := seedTargets(store); err != nil {
		t.Fatalf("seedTargets (2nd): %v", err)
	}

	if _, ok := targetByOSParams(t, store, "flatcar", `{"channel":"stable"}`); !ok {
		t.Error("flatcar predefined target missing (params must carry the flag channel)")
	}
	if _, ok := targetByOSParams(t, store, "fedora-coreos", `{"channel":"stable"}`); !ok {
		t.Error("fedora-coreos predefined target missing (params must carry the flag channel)")
	}
	tal, ok := targetByOSParams(t, store, "talos", `{"schematic":"defschem"}`)
	if !ok {
		t.Fatal("talos predefined target missing")
	}
	if tal.RetainN != 3 || tal.Source != "catalog" {
		t.Errorf("talos target = %+v, want RetainN=3 Source=catalog", tal)
	}
	all, _ := store.ListTargets()
	if len(all) != 3 {
		t.Errorf("ListTargets = %d, want 3 (no duplicates after 2 seeds)", len(all))
	}
}

func TestSeedTargets_HostDerivedTalosSchematic(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosSchematic, "defschem")
	viper.Set(config.TalosRetainMinors, 2)

	// seedTargets reads hosts from the SAME store, so register the Talos host
	// directly on it (no pkg/hardware involvement).
	store := seedTestStore(t)
	if err := store.UpsertHost(db.Host{MAC: "aa:bb:cc:dd:ee:ff", OS: "talos", Schematic: "hostschem"}); err != nil {
		t.Fatalf("seed host: %v", err)
	}

	if err := seedTargets(store); err != nil {
		t.Fatalf("seedTargets: %v", err)
	}

	hd, ok := targetByOSParams(t, store, "talos", `{"schematic":"hostschem"}`)
	if !ok {
		t.Fatal("host-derived talos target missing")
	}
	if hd.Source != "host" {
		t.Errorf("host-derived target should have source=host, got %+v", hd)
	}
	// Confirm the params string is the canonical JSON the UNIQUE constraint sees.
	var m map[string]string
	if err := json.Unmarshal([]byte(hd.Params), &m); err != nil || m["schematic"] != "hostschem" {
		t.Errorf("host-derived params = %q, want canonical {\"schematic\":\"hostschem\"}", hd.Params)
	}
}

// TestSeedTargets_CreateIfAbsent_PatchSurvives: the #48 headline fix — a PATCH
// (retainN, enabled) must survive the next seed pass instead of being silently
// reverted within one tick (pre-#48 UpsertTarget clobbered it).
func TestSeedTargets_CreateIfAbsent_PatchSurvives(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosSchematic, "defschem")
	viper.Set(config.TalosRetainMinors, 3)

	store := seedTestStore(t)
	if err := seedTargets(store); err != nil {
		t.Fatalf("seedTargets: %v", err)
	}
	fl, ok := targetByOSParams(t, store, "flatcar", `{"channel":"stable"}`)
	if !ok {
		t.Fatal("flatcar predefined target missing")
	}
	fl.RetainN = 2 // simulate PATCH /api/v1/targets/{id} {"retainN": 2}
	fl.Enabled = false
	if err := store.UpsertTarget(fl); err != nil {
		t.Fatalf("patch: %v", err)
	}

	if err := seedTargets(store); err != nil { // next tick
		t.Fatalf("seedTargets (2nd): %v", err)
	}
	got, _ := targetByOSParams(t, store, "flatcar", `{"channel":"stable"}`)
	if got.RetainN != 2 || got.Enabled {
		t.Fatalf("seed clobbered the PATCH: %+v (want RetainN=2 Enabled=false)", got)
	}
}

// TestSeedTargets_TalosRetainFlagIsFirstBootOnly: documented D1 behavior change
// — bumping --talosRetainMinors later does NOT update an existing row.
func TestSeedTargets_TalosRetainFlagIsFirstBootOnly(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosSchematic, "defschem")
	viper.Set(config.TalosRetainMinors, 3)

	store := seedTestStore(t)
	if err := seedTargets(store); err != nil {
		t.Fatalf("seedTargets: %v", err)
	}
	viper.Set(config.TalosRetainMinors, 5) // operator bumps the flag later
	if err := seedTargets(store); err != nil {
		t.Fatalf("seedTargets (2nd): %v", err)
	}
	tal, _ := targetByOSParams(t, store, "talos", `{"schematic":"defschem"}`)
	if tal.RetainN != 3 {
		t.Fatalf("talos RetainN = %d, want 3 (flag is first-boot default only; API is the knob)", tal.RetainN)
	}
}

// TestSeedTargets_RejectsUnsafeChannelFlag: a malformed channel flag must not
// mint an unsafe path segment.
func TestSeedTargets_RejectsUnsafeChannelFlag(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	viper.Set(config.FlatcarChannel, "../evil")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosSchematic, "defschem")
	viper.Set(config.TalosRetainMinors, 3)

	store := seedTestStore(t)
	if err := seedTargets(store); err == nil {
		t.Fatal("seedTargets must reject a non-path-safe channel flag")
	}
	all, _ := store.ListTargets()
	if len(all) != 0 {
		t.Fatalf("no targets may be seeded from an unsafe flag, got %d", len(all))
	}
}
