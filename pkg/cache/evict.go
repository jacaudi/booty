package cache

import (
	"fmt"
	"log/slog"

	"github.com/jeefy/booty/pkg/db"
)

// evictOverBudget deletes oldest archived-unpinned cached versions (row + disk
// dir) until total cache size is within maxBytes. It NEVER evicts in-window or
// pinned versions; if only those remain over budget it logs a WARN and stops.
// maxBytes<=0 means unlimited (no-op). Called from reconcileAll on the
// coordinator goroutine.
//
// The no-progress guard: eviction trusts the DB `size` column; a size=0 row
// would free nothing yet keep the loop deleting. D14 (ListArchivedUnpinned's
// size>0 filter) is the primary defense — it excludes zero-byte rows from
// candidacy entirely. This guard is the backstop: each pass still re-checks
// SumCacheBytes and stops if a deletion makes no measurable progress, in case
// the size column is wrong in some other way.
func evictOverBudget(store *db.Store, maxBytes int64) error {
	if maxBytes <= 0 {
		return nil
	}
	// M3: (schematic, version) pairs a live cluster references are never evicted,
	// even when archived and below the discovery window — a parallel never-evict
	// input alongside D13's newest-cached guard (design §8). Resolved once per
	// call; the set is small (one entry per cluster×member-schematic).
	pins, err := clusterPins(store)
	if err != nil {
		return err
	}
	for {
		total, err := store.SumCacheBytes()
		if err != nil {
			return fmt.Errorf("cache: evict sum: %w", err)
		}
		if total <= maxBytes {
			return nil
		}
		candidates, err := store.ListArchivedUnpinned() // oldest fetched_at first, size>0
		if err != nil {
			return fmt.Errorf("cache: evict list: %w", err)
		}
		// D13: never evict the newest cached version of a target — those are the
		// bytes NewestCached serves to the boot path. Version ordering is
		// ostype-specific (not SQL-expressible), so the guard lives here, using
		// NewestCached (the disk-scan authority). Skip protected candidates and
		// take the first evictable one.
		var c *db.CacheEntryRow
		for i := range candidates {
			cand := candidates[i]
			params, _ := decodeParams(cand.Params)
			if cand.Version == NewestCached(canonicalToCacheName(cand.OS), cand.Arch, params) {
				continue
			}
			// M3: never evict a version a live cluster pins.
			if cand.OS == "talos" {
				if _, pinned := pins[SchematicVersion{Schematic: paramSegment(params), Version: cand.Version}]; pinned {
					continue
				}
			}
			c = &candidates[i]
			break
		}
		if c == nil {
			slog.Warn("cache: over budget but only in-window/pinned/newest versions remain; not evicting",
				"totalBytes", total, "maxBytes", maxBytes)
			return nil
		}
		params, _ := decodeParams(c.Params)
		cacheName := canonicalToCacheName(c.OS)
		segment := paramSegment(params)
		if err := store.DeleteTargetVersionByID(c.TargetVersionID); err != nil {
			return fmt.Errorf("cache: evict delete row %s/%s: %w", c.OS, c.Version, err)
		}
		if err := removeVersionDir(cacheName, segment, c.Arch, c.Version); err != nil {
			slog.Warn("cache: evict dir failed", "os", c.OS, "version", c.Version, "err", err)
		}
		slog.Info("cache: evicted archived version", "os", c.OS, "version", c.Version, "bytes", c.Size)

		// No-progress guard: if the sum didn't drop (size=0 row), stop to avoid
		// over-evicting archived rows on bad accounting.
		newTotal, err := store.SumCacheBytes()
		if err != nil {
			return fmt.Errorf("cache: evict resum: %w", err)
		}
		if newTotal >= total {
			slog.Warn("cache: eviction made no measurable progress; stopping", "totalBytes", newTotal)
			return nil
		}
	}
}
