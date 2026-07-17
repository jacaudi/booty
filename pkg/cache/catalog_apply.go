package cache

import (
	"fmt"

	"github.com/jeefy/booty/pkg/db"
)

// applyCatalog reconciles catalog-declared targets to the desired set on the
// reconciler's single-writer coordinator goroutine. It is authoritative ONLY
// for declared fields (enabled, retain_n, source, spec-derived params):
//   - declared entry present  -> create-if-absent, else update declared fields
//     (mode and any non-declared field are preserved); mark source='catalog'.
//   - source='catalog' row not in the desired set -> disable (row + bytes kept).
//   - source='api' / source='host' rows -> never touched.
//
// Identity is (os,arch,params); it is never re-keyed, so nothing re-downloads.
func applyCatalog(store *db.Store, entries []CatalogEntry) error {
	existing, err := store.ListTargets()
	if err != nil {
		return fmt.Errorf("cache: catalog apply: list targets: %w", err)
	}
	byIdentity := make(map[string]db.Target, len(existing))
	for _, t := range existing {
		byIdentity[identityKey(t.OS, t.Arch, t.Params)] = t
	}

	desired := make(map[string]bool, len(entries))
	for _, e := range entries {
		params, err := encodeParams(e.Spec)
		if err != nil {
			return fmt.Errorf("cache: catalog apply: encode params: %w", err)
		}
		key := identityKey(e.OS, e.Arch, params)
		desired[key] = true
		enabled, retain := e.enabledOrDefault(), e.retainOrDefault()

		if cur, ok := byIdentity[key]; ok {
			if cur.Source != "catalog" || cur.Enabled != enabled || cur.RetainN != retain {
				if err := store.UpdateTargetFromCatalog(cur.ID, enabled, retain); err != nil {
					return err
				}
			}
			continue
		}
		if _, err := store.CreateTarget(db.Target{
			OS: e.OS, Arch: e.Arch, Params: params, Mode: "discovery",
			RetainN: retain, Source: "catalog", Enabled: enabled,
		}); err != nil {
			return fmt.Errorf("cache: catalog apply: create %s/%s: %w", e.OS, e.Arch, err)
		}
	}

	for _, t := range existing {
		if t.Source == "catalog" && t.Enabled && !desired[identityKey(t.OS, t.Arch, t.Params)] {
			if err := store.DisableTarget(t.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func identityKey(os, arch, params string) string { return os + "|" + arch + "|" + params }
