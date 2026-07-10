package cache

import (
	"fmt"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// SchematicVersion is one (schematic, version) retention pin — re-exported from
// pkg/db so the cache package's eviction guard keys on the same type the store
// returns (single knowledge site for the pair's shape).
type SchematicVersion = db.SchematicVersion

// clusterPins builds the set of (schematic-segment, version) pairs referenced
// by live clusters — the M3 never-evict input (design §8/D4). An empty
// schematic (a member without one, or a memberless cluster) resolves to the
// --talosSchematic default, the SAME resolution paramSegment/the boot path
// apply, so the pin matches the on-disk cache segment. A nil store yields an
// empty set (no pins), which is safe: eviction just falls back to the D13-only
// behavior.
func clusterPins(store *db.Store) (map[SchematicVersion]struct{}, error) {
	pins := map[SchematicVersion]struct{}{}
	if store == nil {
		return pins, nil
	}
	refs, err := store.ClusterReferencedVersions()
	if err != nil {
		return nil, fmt.Errorf("cache: cluster pins: %w", err)
	}
	def := viper.GetString(config.TalosSchematic)
	for _, r := range refs {
		schematic := r.Schematic
		if schematic == "" {
			schematic = def
		}
		pins[SchematicVersion{Schematic: schematic, Version: r.Version}] = struct{}{}
	}
	return pins, nil
}
