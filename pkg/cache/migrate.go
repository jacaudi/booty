package cache

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// MigrateChannelLayout is the one-time #48 migration, run at startup BEFORE
// the reconciler starts (seedTargets runs inside reconcileAll; the in-place
// rewrite must precede the create-if-absent seed so target_versions and
// cache_entries survive on the same row).
//
// Two steps, INDEPENDENTLY idempotent (crash-consistency: neither keys on the
// other having run, so a crash between them retries the remainder next start):
//
//  1. DB step (keyed on the old shape existing): every flatcar/fedora-coreos
//     target with params="{}" has its params rewritten in place to
//     {"channel": <current flag>}. If the destination (os,arch,params) row
//     already exists (operator pre-created it), the old row is DISABLED and
//     logged — never silently merged.
//  2. Disk step (keyed ONLY on directories, never on DB/params state): per OS
//     root, if <cacheRoot>/<os>/- exists and <cacheRoot>/<os>/<flag-channel>
//     does not, rename it; if both exist, WARN and leave "-" (scan reports
//     orphans; reconcile re-downloads — self-healing).
//
// If the operator changed the flag between runs, the rename mislabels old
// artifacts as the new channel; the reconciler then discovers the real newest
// for that channel and the mislabeled versions age out as archived entries —
// bounded, self-correcting damage (documented in STORAGE.md).
//
// A malformed channel flag fails startup rather than minting an unsafe segment.
func MigrateChannelLayout(store *db.Store) error {
	channelByOS := map[string]string{ // canonical OS -> flag channel
		"flatcar":       viper.GetString(config.FlatcarChannel),
		"fedora-coreos": viper.GetString(config.CoreOSChannel),
	}
	for osName, ch := range channelByOS {
		if err := ValidatePathParam(ch); err != nil {
			return fmt.Errorf("cache: migrate %s channel flag: %w", osName, err)
		}
	}

	// Step 1: DB rewrite (in place, preserving row identity).
	targets, err := store.ListTargets()
	if err != nil {
		return fmt.Errorf("cache: migrate list targets: %w", err)
	}
	// Safety backstop: this index is keyed on the rewrite's distinct (os,arch)
	// pairs, so any unforeseen collision this migration didn't anticipate hits
	// the targets UNIQUE(os,arch,params) constraint on UpdateTargetParams below
	// and aborts startup, rather than silently corrupting a row.
	hasParams := map[string]bool{} // "os|arch|params" existence index
	for _, t := range targets {
		hasParams[t.OS+"|"+t.Arch+"|"+t.Params] = true
	}
	for _, t := range targets {
		ch, ok := channelByOS[t.OS]
		if !ok || t.Params != "{}" {
			continue
		}
		newParams, err := encodeParams(map[string]string{"channel": ch})
		if err != nil {
			return fmt.Errorf("cache: migrate encode params: %w", err)
		}
		if hasParams[t.OS+"|"+t.Arch+"|"+newParams] {
			// Predefined is the one-time "already handled this collision" marker:
			// flipped false the first time this branch disables a row, so later
			// startups skip silently even after an operator re-enables the row via
			// PATCH (which never touches Predefined) — Enabled alone can't carry
			// this because "never touched, freshly enabled" and "disabled once,
			// then re-enabled" are otherwise indistinguishable (D1: API owns rows;
			// migrate must not re-litigate an operator's later decision).
			if !t.Predefined {
				continue
			}
			t.Enabled = false
			t.Predefined = false
			if err := store.UpsertTarget(t); err != nil {
				return fmt.Errorf("cache: migrate disable old %s row: %w", t.OS, err)
			}
			slog.Warn("cache: migrate: destination target already exists; disabled the pre-#48 row (never merged)",
				"os", t.OS, "arch", t.Arch, "oldTargetID", t.ID, "channel", ch)
			continue
		}
		if err := store.UpdateTargetParams(t.ID, newParams); err != nil {
			return fmt.Errorf("cache: migrate rewrite %s params: %w", t.OS, err)
		}
		slog.Info("cache: migrate: rewrote pre-#48 target params in place",
			"os", t.OS, "arch", t.Arch, "target", t.ID, "channel", ch)
	}

	// Step 2: disk rename, keyed only on directories.
	for osName, ch := range channelByOS {
		cacheName := canonicalToCacheName(osName)
		oldDir := filepath.Join(cacheRoot(), cacheName, "-")
		newDir := filepath.Join(cacheRoot(), cacheName, ch)
		if _, err := os.Stat(oldDir); err != nil {
			continue // nothing to migrate
		}
		if _, err := os.Stat(newDir); err == nil {
			slog.Warn("cache: migrate: both '-' and channel dirs exist; leaving '-' (scan reports orphans)",
				"os", osName, "old", oldDir, "new", newDir)
			continue
		}
		if err := os.Rename(oldDir, newDir); err != nil {
			return fmt.Errorf("cache: migrate rename %s: %w", oldDir, err)
		}
		slog.Info("cache: migrate: renamed cache dir to channel segment", "os", osName, "from", oldDir, "to", newDir)
	}
	return nil
}
