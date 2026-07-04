package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
)

type listHostsOutput struct {
	Body struct {
		Hosts []*hardware.Host `json:"hosts"`
	}
}

// validateHostConfigRoles checks an optional config/role binding WITHOUT
// writing anything: configID nil = leave config binding unchanged; roleIDs nil
// = leave roles unchanged. A present configID must exist and satisfy the
// family-match guard for the host's OS; every present roleID must exist.
// Callers that need to guarantee a host is untouched on failure (e.g. approve,
// which must not leave a host approved+assigned when the requested binding is
// invalid) call this BEFORE any other host mutation.
func validateHostConfigRoles(store *db.Store, host *hardware.Host, configID *int64, roleIDs *[]int64) error {
	if configID != nil {
		cfg, err := store.GetConfig(*configID)
		if errors.Is(err, db.ErrNotFound) {
			return huma.Error422UnprocessableEntity("config does not exist")
		}
		if err != nil {
			return huma.Error500InternalServerError("get config", err)
		}
		fam, ok := osFamily(host.OS)
		if !ok || cfg.Kind != configKindForFamily(fam.ConfigKind) {
			return huma.Error422UnprocessableEntity("config kind does not match host OS family")
		}
	}
	if roleIDs != nil {
		for _, rid := range *roleIDs {
			if _, err := store.GetRole(rid); errors.Is(err, db.ErrNotFound) {
				return huma.Error422UnprocessableEntity("role does not exist")
			} else if err != nil {
				return huma.Error500InternalServerError("get role", err)
			}
		}
	}
	return nil
}

// writeHostConfigRoles applies an optional config/role binding to a host,
// mutating host state ONLY through pkg/hardware wrappers. It performs no
// validation — callers MUST call validateHostConfigRoles first (bindHostConfigRoles
// does this for callers that validate and write in the same step).
func writeHostConfigRoles(host *hardware.Host, configID *int64, roleIDs *[]int64) error {
	if configID != nil {
		if err := hardware.SetHostConfig(host.MAC, configID); err != nil {
			return huma.Error500InternalServerError("bind config", err)
		}
	}
	if roleIDs != nil {
		if err := hardware.SetHostRoles(host.MAC, *roleIDs); err != nil {
			return huma.Error500InternalServerError("bind roles", err)
		}
	}
	return nil
}

// bindHostConfigRoles validates and applies an optional config/role binding to a
// host: validate-then-write, so a validation failure (bad/family-mismatched
// config, or a missing role) binds nothing — neither half is left partially
// persisted. Used by /bind, where the host's approval state does not change.
func bindHostConfigRoles(store *db.Store, host *hardware.Host, configID *int64, roleIDs *[]int64) error {
	if err := validateHostConfigRoles(store, host, configID, roleIDs); err != nil {
		return err
	}
	return writeHostConfigRoles(host, configID, roleIDs)
}

func registerHosts(api huma.API, deps APIDeps) {
	// GET /hosts (?approved=)
	huma.Register(api, huma.Operation{
		OperationID: "list-hosts", Method: http.MethodGet, Path: "/hosts",
		Summary: "List known hosts", Tags: []string{"hosts"},
	}, func(ctx context.Context, in *struct {
		// Approved is an optional bool filter ("true"/"false"); omit to list all.
		Approved string `query:"approved"`
	}) (*listHostsOutput, error) {
		// Parse the optional approved filter (Huma v2 does not allow *bool for
		// query params, so we accept a string and parse it here).
		var approvedFilter *bool
		if in.Approved != "" {
			b, err := strconv.ParseBool(in.Approved)
			if err != nil {
				return nil, huma.Error422UnprocessableEntity("approved must be true or false")
			}
			approvedFilter = &b
		}
		hosts, err := hardware.ListHosts()
		if err != nil {
			return nil, huma.Error500InternalServerError("list hosts", err)
		}
		out := &listHostsOutput{}
		for _, h := range hosts {
			if approvedFilter != nil && h.Approved != *approvedFilter {
				continue
			}
			out.Body.Hosts = append(out.Body.Hosts, h)
		}
		return out, nil
	})

	// POST /hosts/{mac}/approve — approve + assign to the host's own OS.
	// Body is OPTIONAL: an empty body is byte-identical to pre-P4 approve
	// behavior. When configId/roleIds are present, approve also atomically
	// binds them via the shared bindHostConfigRoles helper (P4).
	huma.Register(api, huma.Operation{
		OperationID: "approve-host", Method: http.MethodPost, Path: "/hosts/{mac}/approve",
		Summary: "Approve a host", Tags: []string{"hosts"},
	}, func(ctx context.Context, in *struct {
		MAC  string `path:"mac"`
		Body *struct {
			ConfigID *int64   `json:"configId,omitempty"`
			RoleIDs  *[]int64 `json:"roleIds,omitempty"`
		}
	}) (*struct{ Body *hardware.Host }, error) {
		h, err := hardware.GetMacAddress(in.MAC)
		if errors.Is(err, hardware.ErrNotFound) {
			return nil, huma.Error404NotFound("host not found")
		}
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		}
		hasBinding := in.Body != nil && (in.Body.ConfigID != nil || in.Body.RoleIDs != nil)
		// Validate a requested binding BEFORE approving/assigning: a validation
		// failure must leave the host untouched (still pending, no partial
		// approval) rather than approving+assigning it and then erroring on the
		// bind — which would otherwise leave the host approved and booting the
		// server-default config while the caller sees a 422.
		if hasBinding {
			if err := validateHostConfigRoles(deps.Store, h, in.Body.ConfigID, in.Body.RoleIDs); err != nil {
				return nil, err
			}
		}
		if err := hardware.Approve(in.MAC); err != nil {
			return nil, huma.Error500InternalServerError("approve", err)
		}
		// Assign to the host's self-reported OS so it boots that target.
		// approve deliberately sets boot_mode='assigned'; menu mode is set
		// separately via POST /hosts/{mac}/menu.
		if h.OS != "" {
			params := map[string]string{}
			if h.OS == "talos" && h.Schematic != "" {
				params["schematic"] = h.Schematic
			}
			encoded, err := cache.EncodeParams(params)
			if err != nil {
				return nil, huma.Error500InternalServerError("encode params", err)
			}
			if err := hardware.SetAssignment(in.MAC, h.OS, "", encoded); err != nil {
				return nil, huma.Error500InternalServerError("assign", err)
			}
		}
		if hasBinding {
			// Validation already ran above; write only.
			if err := writeHostConfigRoles(h, in.Body.ConfigID, in.Body.RoleIDs); err != nil {
				return nil, err
			}
		}
		updated, err := hardware.GetMacAddress(in.MAC)
		if err != nil {
			return nil, huma.Error500InternalServerError("get updated host", err)
		}
		return &struct{ Body *hardware.Host }{Body: updated}, nil
	})

	// POST /hosts/{mac}/bind — rebind config/roles on an already-approved host
	// without changing approval state.
	huma.Register(api, huma.Operation{
		OperationID: "bind-host", Method: http.MethodPost, Path: "/hosts/{mac}/bind",
		Summary: "Bind config/roles to an approved host", Tags: []string{"hosts"},
	}, func(ctx context.Context, in *struct {
		MAC  string `path:"mac"`
		Body *struct {
			ConfigID *int64   `json:"configId,omitempty"`
			RoleIDs  *[]int64 `json:"roleIds,omitempty"`
		}
	}) (*struct{ Body *hardware.Host }, error) {
		h, err := hardware.GetMacAddress(in.MAC)
		if errors.Is(err, hardware.ErrNotFound) {
			return nil, huma.Error404NotFound("host not found")
		}
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		}
		if in.Body != nil {
			if err := bindHostConfigRoles(deps.Store, h, in.Body.ConfigID, in.Body.RoleIDs); err != nil {
				return nil, err
			}
		}
		updated, err := hardware.GetMacAddress(in.MAC)
		if err != nil {
			return nil, huma.Error500InternalServerError("get updated host", err)
		}
		return &struct{ Body *hardware.Host }{Body: updated}, nil
	})

	// POST /hosts/{mac}/revoke
	huma.Register(api, huma.Operation{
		OperationID: "revoke-host", Method: http.MethodPost, Path: "/hosts/{mac}/revoke",
		Summary: "Revoke a host", Tags: []string{"hosts"},
	}, func(ctx context.Context, in *struct {
		MAC string `path:"mac"`
	}) (*struct{}, error) {
		if err := hardware.Revoke(in.MAC); err != nil {
			return nil, huma.Error422UnprocessableEntity("revoke", err)
		}
		return nil, nil
	})

	// POST /hosts/{mac}/menu — approve (if needed) + set boot_mode='menu'.
	// MUST NOT route through hardware.SetAssignment (which sets boot_mode='assigned'
	// and would clobber menu mode). OPEN in the trust window like approve/revoke.
	huma.Register(api, huma.Operation{
		OperationID: "menu-host", Method: http.MethodPost, Path: "/hosts/{mac}/menu",
		Summary: "Put a host into interactive boot-menu mode", Tags: []string{"hosts"},
	}, func(ctx context.Context, in *struct {
		MAC string `path:"mac"`
	}) (*struct{ Body *hardware.Host }, error) {
		if _, err := hardware.GetMacAddress(in.MAC); errors.Is(err, hardware.ErrNotFound) {
			return nil, huma.Error404NotFound("host not found")
		} else if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		}
		if err := hardware.Approve(in.MAC); err != nil {
			return nil, huma.Error500InternalServerError("approve", err)
		}
		if err := hardware.SetBootMode(in.MAC, "menu"); err != nil {
			return nil, huma.Error500InternalServerError("set menu mode", err)
		}
		updated, err := hardware.GetMacAddress(in.MAC)
		if err != nil {
			return nil, huma.Error500InternalServerError("get updated host", err)
		}
		return &struct{ Body *hardware.Host }{Body: updated}, nil
	})

	// PUT/DELETE /hosts/{mac} — wired-but-403 until auth.
	huma.Register(api, huma.Operation{
		OperationID: "put-host", Method: http.MethodPut, Path: "/hosts/{mac}",
		Summary: "Edit a host (disabled until auth)", Tags: []string{"hosts"},
	}, func(ctx context.Context, _ *struct {
		MAC string `path:"mac"`
	}) (*struct{}, error) {
		return nil, huma.Error403Forbidden("destructive endpoints are disabled until authentication lands (P10)")
	})
	huma.Register(api, huma.Operation{
		OperationID: "delete-host", Method: http.MethodDelete, Path: "/hosts/{mac}",
		Summary: "Delete a host (disabled until auth)", Tags: []string{"hosts"},
	}, func(ctx context.Context, _ *struct {
		MAC string `path:"mac"`
	}) (*struct{}, error) {
		return nil, huma.Error403Forbidden("destructive endpoints are disabled until authentication lands (P10)")
	})
}
