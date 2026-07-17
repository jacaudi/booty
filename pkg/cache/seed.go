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

// reconcileHostSchematics ensures one Talos discovery target per distinct
// host-configured schematic (source='host'), excluding the flag default (which
// the catalog owns). It is the surviving half of the retired seedTargets: the
// catalog-apply pass replaced the static predefined slice, but host-derived
// schematics are not catalog-expressible and remain reconciled from registered
// hosts. An invalid host schematic is skipped with a warning (data problem —
// must not block the pass).
func reconcileHostSchematics(store *db.Store) error {
	defaultSchematic := viper.GetString(config.TalosSchematic)
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
			return fmt.Errorf("cache: ensure host schematic %s: %w", schematic, err)
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
