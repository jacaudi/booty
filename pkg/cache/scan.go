package cache

import (
	"fmt"
	"os"
	"strings"

	"github.com/jeefy/booty/pkg/db"
)

// ScanResult summarizes a disk↔DB cache reconciliation.
type ScanResult struct {
	Scanned int `json:"scanned"` // cached versions examined
	Updated int `json:"updated"` // rows whose size was recomputed or repaired
	Orphans int `json:"orphans"` // on-disk version dirs with no target_version
}

// Scan reconciles the on-disk cache against the DB. For each target_version with
// cached=1 it recomputes size from disk and repairs a missing cache_entries row
// (defaulting in_window=1 — scan does NOT compute window membership, which needs
// live discovery; the next reconcile self-heals in_window). On-disk version dirs
// with no matching target_version are counted as orphans (reported, never
// auto-adopted). Runs via the store from the caller (write serialization is
// SetMaxOpenConns(1)).
func Scan(store *db.Store) (ScanResult, error) {
	var res ScanResult
	targets, err := store.ListTargets()
	if err != nil {
		return res, fmt.Errorf("cache: scan list targets: %w", err)
	}
	// Index cached (target_version) tuples so we can detect orphan dirs.
	type key struct{ cacheName, segment, arch, version string }
	known := map[key]bool{}
	for _, t := range targets {
		params, _ := decodeParams(t.Params)
		cacheName := canonicalToCacheName(t.OS)
		segment := paramSegment(params)
		versions, err := store.ListTargetVersions(t.ID)
		if err != nil {
			return res, fmt.Errorf("cache: scan versions %d: %w", t.ID, err)
		}
		for _, v := range versions {
			if !v.Cached {
				continue
			}
			known[key{cacheName, segment, t.Arch, v.Version}] = true
			dir := cacheDir(cacheName, segment, t.Arch, v.Version)
			var size int64
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.HasSuffix(strings.ToLower(e.Name()), ".partial") {
					continue // in-flight download, not a cached artifact
				}
				if fi, err := e.Info(); err == nil {
					size += fi.Size()
				}
			}
			res.Scanned++
			if err := store.UpsertCacheEntry(v.ID, size); err != nil { // repairs missing row; in_window handled by reconcile
				return res, fmt.Errorf("cache: scan upsert %d/%s: %w", t.ID, v.Version, err)
			}
			res.Updated++
		}
	}
	// Count orphan disk dirs (present on disk, not a known cached tuple).
	for _, e := range ListCached() {
		if !known[key{e.CacheName, e.Segment, e.Arch, e.Version}] {
			res.Orphans++
		}
	}
	return res, nil
}
