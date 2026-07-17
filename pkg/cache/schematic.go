package cache

import (
	"fmt"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// EnsureSchematicTarget idempotently creates the Talos discovery cache target
// for one schematic ID so the reconciler eagerly fetches its boot assets
// (kernel-<arch>, initramfs-<arch>.xz) across the retained version window
// (P5 design D4). It is the single knowledge site for the talos-schematic
// target row shape: seedTargets' host-derived loop and the schematic-save API
// path are two triggers for the same "ensure a discovery target for schematic
// X" knowledge, both funnelling through db.EnsureTarget (create-if-absent,
// #48 D1 — an existing row's mode/retain_n/enabled stay API-owned).
//
// ponytail: schematic-derived rows inherit the host-derived behavior noted in
// seedTargets — NOT pruned when the schematic config is later deleted (DELETE
// is 403 until P10 anyway); a stale target only over-caches (SGE M4).
func EnsureSchematicTarget(store *db.Store, schematic string) error {
	if err := ValidatePathParam(schematic); err != nil {
		return fmt.Errorf("cache: schematic target: %w", err)
	}
	params, err := encodeParams(map[string]string{"schematic": schematic})
	if err != nil {
		return fmt.Errorf("cache: encode params: %w", err)
	}
	if err := store.EnsureTarget(db.Target{
		OS: "talos", Arch: viper.GetString(config.TalosArchitecture), Params: params,
		Mode: "discovery", RetainN: viper.GetInt(config.TalosRetainMinors),
		Source: "host", Enabled: true,
	}); err != nil {
		return fmt.Errorf("cache: ensure schematic target %s: %w", schematic, err)
	}
	return nil
}
