package cache

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// seedTargets creates the predefined targets (Flatcar, Fedora CoreOS, Talos)
// and one Talos target per distinct host-configured schematic — CREATE-IF-ABSENT
// (#48 D1): flags are first-boot defaults; once a row exists the API owns
// mode/retain_n/enabled and seed never touches it again. Changing a channel
// flag later therefore creates a NEW predefined target for the new channel
// (params are row identity); the old one stays until disabled via PATCH.
// Flag values that become path segments are validated before any row is
// written. It runs every tick; EnsureTarget makes it idempotent.
//
// ponytail: host-derived rows are NOT pruned when a host is later deleted — a
// stale talos schematic target keeps caching until restart. Acceptable for P1b
// (it only over-caches); add deletion-driven pruning when the host API lands
// (P1c) if it matters.
func seedTargets(store *db.Store) error {
	flatcarChannel := viper.GetString(config.FlatcarChannel)
	coreosChannel := viper.GetString(config.CoreOSChannel)
	defaultSchematic := viper.GetString(config.TalosSchematic)
	for _, v := range []string{flatcarChannel, coreosChannel, defaultSchematic} {
		if err := ValidatePathParam(v); err != nil {
			return fmt.Errorf("cache: seed: %w", err)
		}
	}

	flatcarParams, err := encodeParams(map[string]string{"channel": flatcarChannel})
	if err != nil {
		return fmt.Errorf("cache: encode params: %w", err)
	}
	coreosParams, err := encodeParams(map[string]string{"channel": coreosChannel})
	if err != nil {
		return fmt.Errorf("cache: encode params: %w", err)
	}
	defParams, err := encodeParams(map[string]string{"schematic": defaultSchematic})
	if err != nil {
		return fmt.Errorf("cache: encode params: %w", err)
	}

	talosArch := viper.GetString(config.TalosArchitecture)
	predefined := []db.Target{
		{OS: "flatcar", Arch: viper.GetString(config.FlatcarArchitecture), Params: flatcarParams, Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true},
		{OS: "fedora-coreos", Arch: viper.GetString(config.CoreOSArchitecture), Params: coreosParams, Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true},
		{OS: "talos", Arch: talosArch, Params: defParams, Mode: "discovery", RetainN: viper.GetInt(config.TalosRetainMinors), Source: "catalog", Enabled: true},
	}
	for _, t := range predefined {
		if err := store.EnsureTarget(t); err != nil {
			return fmt.Errorf("cache: seed predefined %s: %w", t.OS, err)
		}
	}

	// Host-derived Talos schematics (predefined=false), excluding the default.
	// A host-supplied schematic also becomes a path segment: an invalid one is
	// skipped with a warning (data problem — must not block predefined seeding).
	schematics, err := hostTalosSchematics(store, defaultSchematic)
	if err != nil {
		return fmt.Errorf("cache: read host schematics: %w", err)
	}
	for _, schematic := range schematics {
		if err := ValidatePathParam(schematic); err != nil {
			slog.Warn("cache: skipping host schematic (not path-safe)", "schematic", schematic, "err", err)
			continue
		}
		if err := EnsureSchematicTarget(store, schematic); err != nil {
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
	return slices.Collect(maps.Keys(set)), nil
}
