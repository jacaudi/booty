package http

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/ostype"
)

// TargetDTO is the wire representation of a cache target. Params is decoded
// from the canonical JSON encoding stored in the DB to a plain map for callers.
type TargetDTO struct {
	ID         int64             `json:"id"`
	OS         string            `json:"os"`
	Arch       string            `json:"arch"`
	Params     map[string]string `json:"params"`
	Mode       string            `json:"mode"`
	RetainN    int               `json:"retainN"`
	Predefined bool              `json:"predefined"`
	Enabled    bool              `json:"enabled"`
}

type listTargetsOutput struct {
	Body struct {
		Targets []TargetDTO `json:"targets"`
	}
}

func toTargetDTO(t db.Target) TargetDTO {
	params, _ := cache.DecodeParams(t.Params)
	return TargetDTO{
		ID: t.ID, OS: t.OS, Arch: t.Arch, Params: params, Mode: t.Mode,
		RetainN: t.RetainN, Predefined: t.Predefined, Enabled: t.Enabled,
	}
}

// registerTargets mounts the /targets and /targets/{id}/versions endpoints on
// the /api/v1 group. POST and PATCH are open during the trust window (P10 adds
// auth). DELETE endpoints are wired but return 403 until authentication lands.
func registerTargets(api huma.API, deps APIDeps) {
	trigger := deps.Trigger
	if trigger == nil {
		trigger = func() {}
	}

	// GET /targets
	huma.Register(api, huma.Operation{
		OperationID: "list-targets", Method: http.MethodGet, Path: "/targets",
		Summary: "List cache targets", Tags: []string{"targets"},
	}, func(ctx context.Context, _ *struct{}) (*listTargetsOutput, error) {
		list, err := deps.Store.ListTargets()
		if err != nil {
			return nil, huma.Error500InternalServerError("list targets", err)
		}
		out := &listTargetsOutput{}
		for _, t := range list {
			out.Body.Targets = append(out.Body.Targets, toTargetDTO(t))
		}
		return out, nil
	})

	// GET /targets/{id}
	huma.Register(api, huma.Operation{
		OperationID: "get-target", Method: http.MethodGet, Path: "/targets/{id}",
		Summary: "Get a cache target", Tags: []string{"targets"},
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*struct{ Body TargetDTO }, error) {
		t, err := deps.Store.GetTarget(in.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, huma.Error404NotFound("target not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("get target", err)
		}
		return &struct{ Body TargetDTO }{Body: toTargetDTO(*t)}, nil
	})

	// POST /targets — OPEN during trust window; mutations trigger async reconcile.
	huma.Register(api, huma.Operation{
		OperationID: "create-target", Method: http.MethodPost, Path: "/targets",
		Summary: "Create a cache target", Tags: []string{"targets"},
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct {
		Body struct {
			OS      string            `json:"os"`
			Arch    string            `json:"arch"`
			Params  map[string]string `json:"params,omitempty"`
			Mode    string            `json:"mode" enum:"discovery,manual"`
			RetainN int               `json:"retainN" minimum:"0"`
		}
	}) (*struct{ Body TargetDTO }, error) {
		o, ok := ostype.Lookup(in.Body.OS)
		if !ok {
			return nil, huma.Error422UnprocessableEntity("unknown OS " + in.Body.OS)
		}
		for _, p := range o.RequiredParams() {
			v := in.Body.Params[p]
			if v == "" {
				return nil, huma.Error422UnprocessableEntity("missing required param: " + p)
			}
			// Required params are the path-discriminating ones (schematic/
			// channel): they become cache dir + URL segments, so they must
			// be path-safe (#48 §3).
			if err := cache.ValidatePathParam(v); err != nil {
				return nil, huma.Error422UnprocessableEntity("invalid param "+p, err)
			}
		}
		encoded, err := cache.EncodeParams(in.Body.Params)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid params", err)
		}
		id, err := deps.Store.CreateTarget(db.Target{
			OS: in.Body.OS, Arch: in.Body.Arch, Params: encoded, Mode: in.Body.Mode,
			RetainN: in.Body.RetainN, Predefined: false, Enabled: true,
		})
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("create target (duplicate?)", err)
		}
		trigger()
		t, err := deps.Store.GetTarget(id)
		if err != nil {
			return nil, huma.Error500InternalServerError("read back target", err)
		}
		return &struct{ Body TargetDTO }{Body: toTargetDTO(*t)}, nil
	})

	// PATCH /targets/{id} — partial update of enabled/retainN/mode; OPEN.
	huma.Register(api, huma.Operation{
		OperationID: "patch-target", Method: http.MethodPatch, Path: "/targets/{id}",
		Summary: "Update a cache target", Tags: []string{"targets"},
	}, func(ctx context.Context, in *struct {
		ID   int64 `path:"id"`
		Body struct {
			Enabled *bool   `json:"enabled,omitempty"`
			RetainN *int    `json:"retainN,omitempty"`
			Mode    *string `json:"mode,omitempty" enum:"discovery,manual"`
		}
	}) (*struct{ Body TargetDTO }, error) {
		t, err := deps.Store.GetTarget(in.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, huma.Error404NotFound("target not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("get target", err)
		}
		if in.Body.Enabled != nil {
			t.Enabled = *in.Body.Enabled
		}
		if in.Body.RetainN != nil {
			t.RetainN = *in.Body.RetainN
		}
		if in.Body.Mode != nil {
			t.Mode = *in.Body.Mode
		}
		if err := deps.Store.UpsertTarget(*t); err != nil {
			return nil, huma.Error500InternalServerError("update target", err)
		}
		trigger()
		updated, err := deps.Store.GetTarget(in.ID)
		if err != nil {
			return nil, huma.Error500InternalServerError("read back target", err)
		}
		return &struct{ Body TargetDTO }{Body: toTargetDTO(*updated)}, nil
	})

	// DELETE /targets/{id} — wired-but-403 until authentication lands (P10).
	huma.Register(api, huma.Operation{
		OperationID: "delete-target", Method: http.MethodDelete, Path: "/targets/{id}",
		Summary: "Delete a cache target (disabled until auth)", Tags: []string{"targets"},
	}, func(ctx context.Context, _ *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, huma.Error403Forbidden("destructive endpoints are disabled until authentication lands (P10)")
	})

	// POST /targets/{id}/versions — manual version pin; OPEN.
	huma.Register(api, huma.Operation{
		OperationID: "add-target-version", Method: http.MethodPost, Path: "/targets/{id}/versions",
		Summary: "Pin a manual version on a target", Tags: []string{"targets"},
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct {
		ID   int64 `path:"id"`
		Body struct {
			Version string `json:"version"`
		}
	}) (*struct{}, error) {
		t, err := deps.Store.GetTarget(in.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, huma.Error404NotFound("target not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("get target", err)
		}
		if o, ok := ostype.Lookup(t.OS); ok {
			if verr := o.ValidateVersion(in.Body.Version); verr != nil {
				return nil, huma.Error422UnprocessableEntity("invalid version", verr)
			}
		}
		if err := deps.Store.UpsertTargetVersion(db.TargetVersion{
			TargetID: in.ID, Version: in.Body.Version, Source: "manual",
		}); err != nil {
			return nil, huma.Error500InternalServerError("pin version", err)
		}
		trigger()
		return nil, nil
	})

	// DELETE /targets/{id}/versions/{v} — wired-but-403 until auth (P10).
	huma.Register(api, huma.Operation{
		OperationID: "delete-target-version", Method: http.MethodDelete, Path: "/targets/{id}/versions/{v}",
		Summary: "Delete a target version (disabled until auth)", Tags: []string{"targets"},
	}, func(ctx context.Context, _ *struct {
		ID int64  `path:"id"`
		V  string `path:"v"`
	}) (*struct{}, error) {
		return nil, huma.Error403Forbidden("destructive endpoints are disabled until authentication lands (P10)")
	})
}
