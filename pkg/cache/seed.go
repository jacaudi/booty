package cache

import (
	"fmt"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// seedTargets upserts the predefined targets (Flatcar, Fedora CoreOS, Talos) and
// one Talos target per distinct schematic configured on a registered Talos host,
// preserving the old cron's talosSchematics() behavior. It runs every tick;
// UpsertTarget makes it idempotent. Params use the canonical encoder so equal
// param sets collide on UNIQUE(os,arch,params). Host schematics are read from
// the same store (the reconciler's store IS the host store in production), so
// seed.go has no pkg/hardware dependency.
//
// ponytail: host-derived rows are NOT pruned when a host is later deleted — a
// stale talos schematic target keeps caching until restart. Acceptable for P1b
// (it only over-caches); add deletion-driven pruning when the host API lands
// (P1c) if it matters.
func seedTargets(store *db.Store) error {
	predefined := []db.Target{
		{OS: "flatcar", Arch: viper.GetString(config.FlatcarArchitecture), Params: "{}", Mode: "discovery", RetainN: 1, Predefined: true, Enabled: true},
		{OS: "fedora-coreos", Arch: viper.GetString(config.CoreOSArchitecture), Params: "{}", Mode: "discovery", RetainN: 1, Predefined: true, Enabled: true},
	}
	talosArch := viper.GetString(config.TalosArchitecture)
	talosRetain := viper.GetInt(config.TalosRetainMinors)
	defaultSchematic := viper.GetString(config.TalosSchematic)

	defParams, err := encodeParams(map[string]string{"schematic": defaultSchematic})
	if err != nil {
		return err
	}
	predefined = append(predefined, db.Target{
		OS: "talos", Arch: talosArch, Params: defParams, Mode: "discovery",
		RetainN: talosRetain, Predefined: true, Enabled: true,
	})

	for _, t := range predefined {
		if err := store.UpsertTarget(t); err != nil {
			return fmt.Errorf("cache: seed predefined %s: %w", t.OS, err)
		}
	}

	// Host-derived Talos schematics (predefined=false), excluding the default.
	schematics, err := hostTalosSchematics(store, defaultSchematic)
	if err != nil {
		return fmt.Errorf("cache: read host schematics: %w", err)
	}
	for _, schematic := range schematics {
		params, err := encodeParams(map[string]string{"schematic": schematic})
		if err != nil {
			return err
		}
		if err := store.UpsertTarget(db.Target{
			OS: "talos", Arch: talosArch, Params: params, Mode: "discovery",
			RetainN: talosRetain, Predefined: false, Enabled: true,
		}); err != nil {
			return fmt.Errorf("cache: seed host schematic %s: %w", schematic, err)
		}
	}
	return nil
}

// hostTalosSchematics returns every distinct non-default schematic configured on
// a registered Talos host, read from the store (mirrors the retired
// versions.talosSchematics(), now sourced from SQLite rather than the hardware
// global).
func hostTalosSchematics(store *db.Store, defaultSchematic string) ([]string, error) {
	hosts, err := store.ListHosts()
	if err != nil {
		return nil, err
	}
	set := map[string]struct{}{}
	for _, h := range hosts {
		if h.OS == "talos" && h.Schematic != "" && h.Schematic != defaultSchematic {
			set[h.Schematic] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out, nil
}
