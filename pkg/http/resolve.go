package http

import (
	"encoding/base64"
	"errors"
	"log/slog"

	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/ostype"
)

// resolveConfig resolves a host's DB-bound boot config by precedence rungs 1–2
// (design §5): explicit hosts.config_id, then the host's roles ordered by name
// (first with a non-null default_config_id). It applies the family-match guard:
// a resolved config's kind must equal configKindForFamily(fam.ConfigKind) for
// the host's OS family. On no binding, a missing config/revision, a family
// lookup miss, or a guard mismatch it returns ok=false — the caller then falls
// through to the file path (rungs 3–4), preserving byte-identical unbound boot.
func resolveConfig(store *db.Store, host *hardware.Host) (source []byte, kind string, ok bool) {
	if host == nil {
		return nil, "", false
	}
	fam, famOK := osFamily(host.OS)

	// Rung 1: explicit per-host override.
	if host.ConfigID != nil {
		if src, k, got := loadActive(store, *host.ConfigID); got {
			if famOK && k == configKindForFamily(fam.ConfigKind) {
				return src, k, true
			}
			// F4 / design §5: an EXPLICIT per-host binding whose kind mismatches the
			// host family (or whose family lookup misses) is an operator error to
			// SURFACE, not silently paper over with a role default. Return ok=false
			// immediately so the serving handler falls through to the server-default
			// FILE — never substitute a rung-2 role config for a wrong explicit bind.
			slog.Warn("http: explicit host config kind mismatches family; not substituting role default",
				"mac", host.MAC, "os", host.OS, "configID", *host.ConfigID, "kind", k)
			return nil, "", false
		}
		// A bound-but-unloadable config (missing / no active revision — distinct from
		// a mismatch) is not an explicit-intent error; fall through to rung 2.
	}

	// Rung 2: role default, roles ordered by name (ListHostRoles is name-asc).
	roles, err := store.ListHostRoles(host.MAC)
	if err != nil {
		slog.Warn("http: list host roles failed; falling through", "mac", host.MAC, "err", err)
		return nil, "", false
	}
	for _, r := range roles {
		if !r.DefaultConfigID.Valid {
			continue
		}
		src, k, got := loadActive(store, r.DefaultConfigID.Int64)
		if !got {
			continue
		}
		if famOK && k == configKindForFamily(fam.ConfigKind) {
			return src, k, true
		}
		slog.Warn("http: role-default config kind mismatches host family; skipping",
			"mac", host.MAC, "os", host.OS, "role", r.Name, "kind", k)
	}
	return nil, "", false
}

// osFamily returns the OS family for a host OS name, or ok=false on a lookup miss
// (unidentified / empty OS) — no family constraint can then be evaluated.
func osFamily(osName string) (ostype.Family, bool) {
	o, ok := ostype.Lookup(osName)
	if !ok {
		return ostype.Family{}, false
	}
	return o.Family(), true
}

// loadActive fetches a config's active revision source (base64-decoded) + kind.
func loadActive(store *db.Store, configID int64) (source []byte, kind string, ok bool) {
	cfg, err := store.GetConfig(configID)
	if err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			slog.Warn("http: get config failed", "configID", configID, "err", err)
		}
		return nil, "", false
	}
	rev, err := store.GetActiveRevision(configID)
	if err != nil {
		return nil, "", false // no active revision → not servable
	}
	src, err := base64.StdEncoding.DecodeString(rev.SourceB64)
	if err != nil {
		slog.Warn("http: decode config source failed", "configID", configID, "err", err)
		return nil, "", false
	}
	return src, cfg.Kind, true
}
