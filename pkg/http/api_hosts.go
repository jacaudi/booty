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
		// Unknown carries MACs booty has seen but that are not registered.
		// Retiring GET /booty.json removes the only other surface for
		// them, so this field is what makes that supersession real rather
		// than a loss of visibility into unregistered hosts.
		Unknown []string `json:"unknown"`
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
		if !ok || !familyAllowsKind(fam.ConfigKind, cfg.Kind) {
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
		// []string{} and NOT nil: a nil slice marshals to JSON null, and the
		// UI flatMaps array fields — a null survives as an element and reaches
		// the views. Same footgun as authoringKindsForFamily in render.go.
		out.Body.Unknown = hardware.ListUnknownHosts()
		if out.Body.Unknown == nil {
			out.Body.Unknown = []string{}
		}
		return out, nil
	})

	// POST /hosts — the replacement for the retired POST /register (the old
	// endpoint is superseded by this one, under the authenticated /api/v1
	// surface).
	//
	// The DTO is TIGHTENED, not the verbatim hardware.Host: it carries exactly
	// the fields README documented for /register (mac, hostname, os, and the
	// optional Talos schematic). IgnitionFile is deliberately ABSENT — it was
	// the only request-controlled input to the template.ParseFiles read in
	// ignition.go, and omitting it removes that writer at the source rather
	// than only containing it (pkg/http/datapath.go does the containment half).
	// huma defaults to additionalProperties:false, so a client that sends
	// ignitionFile anyway gets a 422 naming the field.
	//
	// Upsert, not create-only: PUT /hosts/{mac} is still an unimplemented stub,
	// so a 409-on-exists would leave no way to change a host's OS at all.
	huma.Register(api, huma.Operation{
		OperationID: "create-host", Method: http.MethodPost, Path: "/hosts",
		Summary: "Create or update a host", Tags: []string{"hosts"},
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct {
		Body struct {
			MAC       string `json:"mac" required:"true" doc:"Host MAC address"`
			Hostname  string `json:"hostname,omitzero"`
			OS        string `json:"os,omitzero" doc:"flatcar, coreos, talos, ..."`
			Schematic string `json:"schematic,omitzero" doc:"Talos Image Factory schematic ID"`
		}
	}) (*struct {
		Status int
		Body   *hardware.Host
	}, error) {
		// OS is optional, but when supplied it must be a real, known OS --
		// unvalidated, a typo (e.g. "talso" for "talos") was written straight
		// into assigned_os by approve-host and only discovered at netboot
		// time. Validate with osFamily (resolve.go), NOT the bare
		// ostype.Lookup create-target uses: this value flows to osFamily on
		// the BOOT path (resolveConfig -> osFamily(host.OS)), which
		// canonicalizes via cache.CacheNameToCanonical before looking up --
		// the single bridge between booty's short boot vocabulary ("coreos")
		// and ostype's canonical taxonomy ("fedora-coreos"). Validating with
		// the bare lookup instead would reject "coreos", a value that has
		// always booted successfully and that booty uses in its own
		// vocabulary elsewhere (GET /api/v1/info's "coreos" key,
		// --coreosArchitecture) -- stricter than the consumer it feeds.
		if in.Body.OS != "" {
			if _, ok := osFamily(in.Body.OS); !ok {
				return nil, huma.Error422UnprocessableEntity("unknown OS " + in.Body.OS)
			}
		}

		existing, err := hardware.GetMacAddress(in.Body.MAC)
		switch {
		case err == nil:
			// update path
		case errors.Is(err, hardware.ErrNotFound):
			existing = nil
		default:
			// GetMacAddress also returns a NormalizeMAC validation error for a
			// malformed MAC (hardware/mac.go:264-267). That is a CLIENT error,
			// and every sibling handler reports it as 422 (api_hosts.go:135,
			// 197, 236) — mapping it to 500 would be a lie about whose fault
			// it is.
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		}

		// READ-MODIFY-WRITE, and this is load-bearing. db.UpsertHost
		// (pkg/db/host.go:75-94) overwrites hostname, ip, booted,
		// ignition_file, os, do_install and schematic from the incoming row on
		// conflict. Building a fresh Host from the DTO would therefore BLANK
		// the host's IP, last-boot marker, one-shot-install flag, and any
		// operator-set ignition_file on every update. Start from the stored
		// row and overlay only what the request actually supplied.
		merged := hardware.Host{MAC: in.Body.MAC}
		status := http.StatusCreated
		if existing != nil {
			merged = *existing
			status = http.StatusOK
		}
		if in.Body.Hostname != "" {
			merged.Hostname = in.Body.Hostname
		}
		if in.Body.OS != "" {
			merged.OS = in.Body.OS
		}
		if in.Body.Schematic != "" {
			merged.Schematic = in.Body.Schematic
		}

		if err := hardware.WriteMacAddress(in.Body.MAC, merged); err != nil {
			return nil, huma.Error500InternalServerError("write host", err)
		}
		created, err := hardware.GetMacAddress(in.Body.MAC)
		if err != nil {
			return nil, huma.Error500InternalServerError("get created host", err)
		}
		return &struct {
			Status int
			Body   *hardware.Host
		}{Status: status, Body: created}, nil
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
		// HasHost, not GetMacAddress, so a mistyped/absent MAC does not
		// permanently pollute ListUnknownHosts via GetMacAddress's miss-side
		// trackUnknown call. h is then fetched below only once the host is
		// known to exist, which hits the found path (clearUnknown), not the
		// miss path.
		exists, err := hardware.HasHost(in.MAC)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		}
		if !exists {
			return nil, huma.Error404NotFound("host not found")
		}
		h, err := hardware.GetMacAddress(in.MAC)
		if err != nil {
			return nil, huma.Error500InternalServerError("get host", err)
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
		// HasHost, not GetMacAddress: see the identical comment on approve-host.
		exists, err := hardware.HasHost(in.MAC)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		}
		if !exists {
			return nil, huma.Error404NotFound("host not found")
		}
		h, err := hardware.GetMacAddress(in.MAC)
		if err != nil {
			return nil, huma.Error500InternalServerError("get host", err)
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
		// HasHost, not GetMacAddress: see the identical comment on approve-host.
		if exists, err := hardware.HasHost(in.MAC); err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		} else if !exists {
			return nil, huma.Error404NotFound("host not found")
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

	// PUT /hosts/{mac} — wired-but-not-implemented.
	huma.Register(api, huma.Operation{
		OperationID: "put-host", Method: http.MethodPut, Path: "/hosts/{mac}",
		Summary: "Edit a host (not implemented)", Tags: []string{"hosts"},
	}, func(ctx context.Context, _ *struct {
		MAC string `path:"mac"`
	}) (*struct{}, error) {
		return nil, huma.Error403Forbidden(msgDestructiveNotImplemented)
	})

	// DELETE /hosts/{mac} — implemented, unlike the other destructive stubs.
	// POST /unregister was retired on the grounds that this endpoint
	// supersedes it, so leaving THIS one a 403 too would delete host
	// deletion as a capability entirely, not just relocate it. It is also
	// the cheapest of the eight by far: hardware.RemoveMacAddress
	// (hardware/mac.go:316) over db.DeleteHost (db/host.go:96) already exists
	// and is already tested, with no cascade semantics to design.
	huma.Register(api, huma.Operation{
		OperationID: "delete-host", Method: http.MethodDelete, Path: "/hosts/{mac}",
		Summary: "Delete a host", Tags: []string{"hosts"},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *struct {
		MAC string `path:"mac"`
	}) (*struct{}, error) {
		// RemoveMacAddress is idempotent, so check existence first to give a
		// truthful 404 rather than a 204 for a MAC that was never there.
		// HasHost, not GetMacAddress: see the identical comment on approve-host
		// -- a mistyped MAC here must not permanently add itself to
		// ListUnknownHosts.
		if exists, err := hardware.HasHost(in.MAC); err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		} else if !exists {
			return nil, huma.Error404NotFound("host not found")
		}
		if err := hardware.RemoveMacAddress(in.MAC); err != nil {
			return nil, huma.Error500InternalServerError("delete host", err)
		}
		return nil, nil
	})
}
