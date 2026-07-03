package cache

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/ostype"
	"github.com/spf13/viper"
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
	// D17: fetch the FCOS channel streams doc at most once per pass; reset the
	// memo at pass entry so a later pass resolves new builds against a fresh doc.
	ostype.ResetStreamsCache()

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
		discovered, derr := o.DiscoverVersions(ctx, params)
		if derr != nil {
			slog.Warn("cache: discovery failed; keeping existing cached set", "os", t.OS, "target", t.ID, "err", derr)
		} else {
			// #48 §8: the retention window ranges over discovered ∪ (in-window
			// AND cached AND discovered), so single-version-discovery OSes
			// (flatcar/fcos) accumulate history release-by-release under
			// retainN>1. The in-window+cached+discovered source keeps archived
			// versions from resurrecting, is the guard P3b's bytes-less failure
			// rows rely on, and excludes manual pins — always desired, never
			// archived, so counting them would only displace a discovered
			// version instead of adding coverage. Evicted versions cannot
			// return: eviction deletes the target_versions row entirely.
			inWindow, werr := store.ListCachedInWindowVersions(t.ID)
			if werr != nil {
				return fmt.Errorf("cache: list in-window %d: %w", t.ID, werr)
			}
			known := slices.Clone(discovered)
			for _, v := range inWindow {
				if !slices.Contains(known, v) {
					known = append(known, v)
				}
			}
			retained = retentionFor(t.OS, known, t.RetainN)
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
		arts, aerr := o.Artifacts(ctx, version, t.Arch, params)
		if aerr != nil {
			slog.Warn("cache: artifacts unavailable; skipping version this tick", "os", t.OS, "version", version, "err", aerr)
			continue
		}
		policy := viper.GetString(config.SignaturePolicy)
		verdicts := make([]artifactVerdict, len(arts))
		landedFlags := make([]bool, len(arts))
		vg := new(errgroup.Group)
		vg.SetLimit(max(concurrency, 1))
		for i, a := range arts {
			vg.Go(func() error {
				landed, v, err := landArtifact(ctx, dir, a, policy)
				if err != nil {
					slog.Warn("cache: artifact fetch failed", "os", t.OS, "version", version, "file", a.Filename, "err", err)
					return err
				}
				verdicts[i], landedFlags[i] = v, landed
				return nil
			})
		}
		if vg.Wait() != nil {
			continue // transport error → whole version retried next tick (nothing recorded)
		}

		tvID, verr := store.TargetVersionID(t.ID, version)
		if verr != nil {
			return fmt.Errorf("cache: resolve tv id %d/%s: %w", t.ID, version, verr)
		}
		verified, verifyErr := aggregateVerdicts(verdicts)

		if slices.Contains(landedFlags, false) {
			// Version REJECTED (a failure the policy refuses to land). Version-level
			// atomicity: wipe the partial-or-landed dir so NewestCached falls back
			// to the prior cached version (§6), and record a failure-visibility row.
			if err := removeVersionDir(cacheName, segment, t.Arch, version); err != nil {
				slog.Warn("cache: remove rejected version dir failed", "os", t.OS, "version", version, "err", err)
			}
			if err := store.UpsertCacheEntryArchived(tvID, verifyErr); err != nil {
				return fmt.Errorf("cache: record rejected %d/%s: %w", t.ID, version, err)
			}
			slog.Error("cache: version rejected by verification", "os", t.OS, "version", version, "policy", policy, "err", verifyErr)
			continue
		}

		// All artifacts landed → mark cached, record size + verdict.
		if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: t.ID, Version: version, Source: source, Cached: true}); err != nil {
			return fmt.Errorf("cache: mark cached %d/%s: %w", t.ID, version, err)
		}
		var size int64
		for _, a := range arts {
			p, perr := artifactPath(dir, a.URL)
			if perr != nil {
				continue
			}
			if fi, serr := os.Stat(p); serr == nil {
				size += fi.Size()
			}
		}
		if err := store.UpsertCacheEntry(tvID, size); err != nil {
			return fmt.Errorf("cache: upsert cache_entry %d/%s: %w", t.ID, version, err)
		}
		if verified != nil { // NULL (off / not-verifiable) leaves the P3a column untouched
			if err := store.SetCacheVerified(tvID, verified, verifyErr); err != nil {
				return fmt.Errorf("cache: record verdict %d/%s: %w", t.ID, version, err)
			}
		}
	}

	// P3a: rotated-out DISCOVERED versions are ARCHIVED (in_window=0), not deleted
	// — disk is kept so they stay menu-bootable (rollback); size-based eviction
	// (evict.go) reclaims oldest archived-unpinned over cacheMaxBytes. Manual rows
	// are never touched. Mark rotated-out discovered rows archived;
	// SetCacheInWindow is a no-op when no cache_entries row exists yet.
	if pruneDiscovered {
		for _, v := range existing {
			if v.Source != "discovered" || slices.Contains(retained, v.Version) {
				continue
			}
			if err := store.SetCacheInWindow(v.ID, false); err != nil {
				return fmt.Errorf("cache: archive %d/%s: %w", t.ID, v.Version, err)
			}
		}
	}
	return nil
}
