package cache

import (
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestEnsureSchematicTargetIdempotent(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosRetainMinors, 3)
	store := seedTestStore(t)

	if err := EnsureSchematicTarget(store, "a1b2c3d4"); err != nil {
		t.Fatalf("EnsureSchematicTarget: %v", err)
	}
	if err := EnsureSchematicTarget(store, "a1b2c3d4"); err != nil {
		t.Fatalf("second EnsureSchematicTarget: %v", err)
	}

	tg, ok := targetByOSParams(t, store, "talos", `{"schematic":"a1b2c3d4"}`)
	if !ok {
		t.Fatal("schematic target not created")
	}
	if tg.Arch != "amd64" || tg.Mode != "discovery" || tg.RetainN != 3 || tg.Predefined || !tg.Enabled {
		t.Fatalf("target row = %+v, want amd64/discovery/3/predefined=false/enabled", tg)
	}
	all, err := store.ListTargets()
	if err != nil || len(all) != 1 {
		t.Fatalf("targets = %d (err %v), want exactly 1 (idempotent)", len(all), err)
	}
}

func TestEnsureSchematicTargetRejectsUnsafeParam(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	store := seedTestStore(t)
	if err := EnsureSchematicTarget(store, "../evil"); err == nil {
		t.Fatal("path-unsafe schematic must be rejected")
	}
	if all, _ := store.ListTargets(); len(all) != 0 {
		t.Fatalf("no row may be written for an unsafe schematic, got %d", len(all))
	}
}
