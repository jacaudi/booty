package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/hardware"
)

type listHostsOutput struct {
	Body struct {
		Hosts []*hardware.Host `json:"hosts"`
	}
}

func registerHosts(api huma.API, _ APIDeps) {
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
	huma.Register(api, huma.Operation{
		OperationID: "approve-host", Method: http.MethodPost, Path: "/hosts/{mac}/approve",
		Summary: "Approve a host", Tags: []string{"hosts"},
	}, func(ctx context.Context, in *struct {
		MAC string `path:"mac"`
	}) (*struct{ Body *hardware.Host }, error) {
		h, err := hardware.GetMacAddress(in.MAC)
		if errors.Is(err, hardware.ErrNotFound) {
			return nil, huma.Error404NotFound("host not found")
		}
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		}
		if err := hardware.Approve(in.MAC); err != nil {
			return nil, huma.Error500InternalServerError("approve", err)
		}
		// Assign to the host's self-reported OS so it boots that target
		// (keeps boot_mode='menu' unreachable in P1c — menu deferred).
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
