package cache

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/ostype"
	"golang.org/x/sync/errgroup"
)

// reconcileTarget brings ONE target's cache into its desired state. It is called
// only from the reconcile coordinator goroutine, so every DB write here is
// single-threaded (no viper/db races). Failures are non-fatal: a discovery
// fetch error keeps the existing cached set (no prune); a per-artifact download
// error is logged and retried next tick.
//
// Desired set: discovery mode -> DiscoverVersions -> retentionFor, plus any
// existing manual rows; manual mode -> just the existing manual rows. discovered
// rows outside the retained set are pruned (row + dir); manual rows are NEVER
// pruned.
//
// concurrency bounds artifact downloads. A FRESH errgroup.Group is created per
// version (errgroup's error is set once and never reset, so a single shared,
// reused group would poison every later Wait); the coordinator runs targets
// sequentially, so a per-version cap is functionally identical to a global cap
// at booty's ~3 upstreams.
func reconcileTarget(ctx context.Context, store *db.Store, concurrency int, t db.Target) error {
	o, ok := ostype.Lookup(t.OS) // t.OS is the canonical taxonomy name
	if !ok {
		return fmt.Errorf("cache: unknown OS %q for target %d", t.OS, t.ID)
	}
	params, err := decodeParams(t.Params)
	if err != nil {
		return fmt.Errorf("cache: target %d params: %w", t.ID, err)
	}

	existing, err := store.ListTargetVersions(t.ID)
	if err != nil {
		return fmt.Errorf("cache: list versions for target %d: %w", t.ID, err)
	}
	var manual []string
	for _, v := range existing {
		if v.Source == "manual" {
			manual = append(manual, v.Version)
		}
	}

	// Resolve the retained discovered set (empty for manual-only targets, and
	// left empty on a discovery-fetch failure so nothing is pruned).
	var retained []string
	pruneDiscovered := false
	if t.Mode == "discovery" {
		discovered, derr := o.DiscoverVersions(ctx)
		if derr != nil {
			slog.Warn("cache: discovery failed; keeping existing cached set", "os", t.OS, "target", t.ID, "err", derr)
		} else {
			retained = retentionFor(t.OS, discovered, t.RetainN)
			pruneDiscovered = true
		}
	}

	cacheName := canonicalToCacheName(t.OS)
	segment := paramSegment(params)

	// Upsert + ensure-artifacts for every desired version (retained discovered +
	// all manual pins). Manual rows keep source="manual".
	desired := append(slices.Clone(retained), manual...)
	for _, version := range desired {
		source := "discovered"
		if slices.Contains(manual, version) {
			source = "manual"
		}
		if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: t.ID, Version: version, Source: source}); err != nil {
			return fmt.Errorf("cache: upsert %d/%s: %w", t.ID, version, err)
		}
		dir := cacheDir(cacheName, segment, t.Arch, version)
		vg := new(errgroup.Group)
		vg.SetLimit(max(concurrency, 1))
		for _, a := range o.Artifacts(version, t.Arch, params) {
			vg.Go(func() error {
				if err := ensureArtifact(ctx, dir, a.URL); err != nil {
					slog.Warn("cache: artifact fetch failed", "os", t.OS, "version", version, "file", a.Filename, "err", err)
					return err
				}
				return nil
			})
		}
		// A fresh group per version scopes errgroup's sticky error to THIS
		// version: one bad artifact never blocks caching later versions/targets.
		// cached=1 only when all of this version's artifacts are present.
		//
		// ponytail: `cached` is a COARSE boolean, re-derived from scratch every
		// reconcile — the boot path reads the disk (NewestCached), not this flag,
		// so it never needs precise per-artifact state. P3's cache_entries table
		// owns size/verification/per-artifact detail; don't build that here.
		if vg.Wait() == nil {
			if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: t.ID, Version: version, Source: source, Cached: true}); err != nil {
				return fmt.Errorf("cache: mark cached %d/%s: %w", t.ID, version, err)
			}
		}
	}

	// Prune discovered rows outside the retained set (never manual). Skipped when
	// discovery failed, so a transient upstream outage does not evict the cache.
	if pruneDiscovered {
		for _, v := range existing {
			if v.Source != "discovered" || slices.Contains(retained, v.Version) {
				continue
			}
			if err := store.DeleteTargetVersion(t.ID, v.Version); err != nil {
				return fmt.Errorf("cache: prune row %d/%s: %w", t.ID, v.Version, err)
			}
			if err := removeVersionDir(cacheName, segment, t.Arch, v.Version); err != nil {
				slog.Warn("cache: prune dir failed", "os", t.OS, "version", v.Version, "err", err)
			}
		}
	}
	return nil
}
