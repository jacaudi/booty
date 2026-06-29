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

	if _, ok := targetByOSParams(t, store, "flatcar", "{}"); !ok {
		t.Error("flatcar predefined target missing")
	}
	if _, ok := targetByOSParams(t, store, "fedora-coreos", "{}"); !ok {
		t.Error("fedora-coreos predefined target missing")
	}
	tal, ok := targetByOSParams(t, store, "talos", `{"schematic":"defschem"}`)
	if !ok {
		t.Fatal("talos predefined target missing")
	}
	if tal.RetainN != 3 || !tal.Predefined {
		t.Errorf("talos target = %+v, want RetainN=3 Predefined=true", tal)
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
	if hd.Predefined {
		t.Errorf("host-derived target should have predefined=false, got %+v", hd)
	}
	// Confirm the params string is the canonical JSON the UNIQUE constraint sees.
	var m map[string]string
	if err := json.Unmarshal([]byte(hd.Params), &m); err != nil || m["schematic"] != "hostschem" {
		t.Errorf("host-derived params = %q, want canonical {\"schematic\":\"hostschem\"}", hd.Params)
	}
}
