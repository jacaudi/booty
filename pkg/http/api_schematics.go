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
// P6 seam (SGE I2): when P6's migration 0005 adds hosts.cluster_id, THIS
// handler gains one additive guard — refuse when the host is a cluster member
// (cluster_id set) — because a member's schematic is single-sourced through
// P6's add-member/regenerate path (the frozen install.image and the pinned
// netboot version must move together). The column does not exist yet, so the
// guard cannot be coded in P5.
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
		if (in.Body.ConfigID == nil) == (in.Body.Schematic == "") {
			return nil, huma.Error422UnprocessableEntity("exactly one of configId or schematic is required")
		}

		id := in.Body.Schematic
		if in.Body.ConfigID != nil {
			cfg, err := deps.Store.GetConfig(*in.Body.ConfigID)
			if errors.Is(err, db.ErrNotFound) {
				return nil, huma.Error422UnprocessableEntity("config does not exist")
			}
			if err != nil {
				return nil, huma.Error500InternalServerError("get config", err)
			}
			if cfg.Kind != "schematic" {
				return nil, huma.Error422UnprocessableEntity("config is not a schematic")
			}
			rev, err := deps.Store.GetActiveRevision(*in.Body.ConfigID)
			if err != nil || rev.DerivedSchematicID == nil {
				return nil, huma.Error422UnprocessableEntity("schematic config has no built revision")
			}
			id = *rev.DerivedSchematicID
		}
		// The bound value becomes a cache path segment + factory URL segment.
		if err := cache.ValidatePathParam(id); err != nil {
			return nil, huma.Error422UnprocessableEntity("schematic id is not path-safe", err)
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
