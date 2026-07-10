package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
)

// registerSchematics mounts the schematic host-binding operation. It is a
// dedicated registrar (not part of registerHosts' config/role binding) because
// schematic binding is a different contract: talos-only, bound by the natural
// content-addressed sha256 (design D3 — no surrogate FK), written to
// host.Schematic so the boot path is untouched.
//
// P6 seam (SGE I2 — now active, P6's migration 0005 added hosts.cluster_id):
// this handler refuses to bind a raw schematic when the host is a cluster
// member (cluster_id set) — a member's schematic is single-sourced through
// P6's add-member/regenerate path (the frozen install.image and the pinned
// netboot version must move together).
func registerSchematics(api huma.API, deps APIDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "bind-host-schematic", Method: http.MethodPost, Path: "/hosts/{mac}/schematic",
		Summary: "Bind a Talos schematic to a host", Tags: []string{"hosts"},
	}, func(ctx context.Context, in *struct {
		MAC  string `path:"mac"`
		Body struct {
			// ConfigID names a schematic-kind config; its CURRENT active
			// revision's derived ID is bound (design D3: an edited schematic
			// rolls a host forward only on explicit re-bind).
			ConfigID *int64 `json:"configId,omitempty"`
			// Schematic is the free-entry escape hatch: a raw content-addressed
			// ID not in the registry (the registry is advisory, design §5).
			Schematic string `json:"schematic,omitempty"`
		}
	}) (*struct{ Body *hardware.Host }, error) {
		h, err := hardware.GetMacAddress(in.MAC)
		if errors.Is(err, hardware.ErrNotFound) {
			return nil, huma.Error404NotFound("host not found")
		}
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		}
		// Mirrors the approve path's literal h.OS == "talos" (api_hosts.go):
		// only Talos hosts carry a schematic.
		if h.OS != "talos" {
			return nil, huma.Error422UnprocessableEntity("schematic binding applies to Talos hosts only")
		}
		if h.ClusterID != nil {
			return nil, huma.Error422UnprocessableEntity("host is a cluster member; change its schematic via the cluster add-member path")
		}
		id, err := resolveSchematicID(deps.Store, in.Body.ConfigID, in.Body.Schematic)
		if err != nil {
			return nil, err
		}
		if err := hardware.SetSchematic(in.MAC, id); err != nil {
			return nil, huma.Error500InternalServerError("bind schematic", err)
		}
		updated, err := hardware.GetMacAddress(in.MAC)
		if err != nil {
			return nil, huma.Error500InternalServerError("get updated host", err)
		}
		return &struct{ Body *hardware.Host }{Body: updated}, nil
	})
}

// resolveSchematicID resolves a schematic binding to its content-addressed ID:
// a configID names a schematic-kind config (its CURRENT active revision's
// derived ID is bound), else raw is a free-entry ID. Exactly one must be
// supplied. The result is path-validated (it becomes a cache + factory URL
// segment). Extracted when the P6 add-member path became a second consumer
// (DRY: single source for "schematic binding -> ID").
func resolveSchematicID(store *db.Store, configID *int64, raw string) (string, error) {
	if (configID == nil) == (raw == "") {
		return "", huma.Error422UnprocessableEntity("exactly one of configId or schematic is required")
	}
	id := raw
	if configID != nil {
		cfg, err := store.GetConfig(*configID)
		if errors.Is(err, db.ErrNotFound) {
			return "", huma.Error422UnprocessableEntity("config does not exist")
		}
		if err != nil {
			return "", huma.Error500InternalServerError("get config", err)
		}
		if cfg.Kind != "schematic" {
			return "", huma.Error422UnprocessableEntity("config is not a schematic")
		}
		rev, err := store.GetActiveRevision(*configID)
		if err != nil || rev.DerivedSchematicID == nil {
			return "", huma.Error422UnprocessableEntity("schematic config has no built revision")
		}
		id = *rev.DerivedSchematicID
	}
	if err := cache.ValidatePathParam(id); err != nil {
		return "", huma.Error422UnprocessableEntity("schematic id is not path-safe", err)
	}
	return id, nil
}
