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
// would free nothing yet keep the loop deleting. So each pass re-checks
// SumCacheBytes and stops if a deletion makes no measurable progress.
func evictOverBudget(store *db.Store, maxBytes int64) error {
	if maxBytes <= 0 {
		return nil
	}
	for {
		total, err := store.SumCacheBytes()
		if err != nil {
			return fmt.Errorf("cache: evict sum: %w", err)
		}
		if total <= maxBytes {
			return nil
		}
		candidates, err := store.ListArchivedUnpinned() // oldest fetched_at first
		if err != nil {
			return fmt.Errorf("cache: evict list: %w", err)
		}
		if len(candidates) == 0 {
			slog.Warn("cache: over budget but only in-window/pinned versions remain; not evicting",
				"totalBytes", total, "maxBytes", maxBytes)
			return nil
		}
		c := candidates[0]
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
