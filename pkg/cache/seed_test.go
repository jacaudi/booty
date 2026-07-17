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

func TestReconcileHostSchematics_HostDerivedTalosSchematic(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosSchematic, "defschem")
	viper.Set(config.TalosRetainMinors, 3)

	// reconcileHostSchematics reads hosts from the SAME store, so register the
	// Talos host directly on it (no pkg/hardware involvement).
	store := seedTestStore(t)
	if err := store.UpsertHost(db.Host{MAC: "aa:bb:cc:dd:ee:ff", OS: "talos", Schematic: "hostschem"}); err != nil {
		t.Fatalf("seed host: %v", err)
	}

	if err := reconcileHostSchematics(store); err != nil {
		t.Fatalf("reconcileHostSchematics: %v", err)
	}

	hd, ok := targetByOSParams(t, store, "talos", `{"schematic":"hostschem"}`)
	if !ok {
		t.Fatal("host-derived talos target missing")
	}
	if hd.Source != "host" || hd.Mode != "discovery" || hd.RetainN != 3 {
		t.Errorf("host-derived target = %+v, want Source=host Mode=discovery RetainN=3", hd)
	}
	// Confirm the params string is the canonical JSON the UNIQUE constraint sees.
	var m map[string]string
	if err := json.Unmarshal([]byte(hd.Params), &m); err != nil || m["schematic"] != "hostschem" {
		t.Errorf("host-derived params = %q, want canonical {\"schematic\":\"hostschem\"}", hd.Params)
	}
}
