package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/db"
)

// RoleDTO is the wire shape of a role.
type RoleDTO struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	DefaultConfigID *int64 `json:"defaultConfigId,omitempty"`
	HostCount       int    `json:"hostCount"`
}

func toRoleDTO(r db.RoleListRow) RoleDTO {
	dto := RoleDTO{ID: r.ID, Name: r.Name, HostCount: r.HostCount}
	if r.DefaultConfigID.Valid {
		v := r.DefaultConfigID.Int64
		dto.DefaultConfigID = &v
	}
	return dto
}

// registerRoles mounts /roles on the /api/v1 group. DELETE is wired-but-403.
func registerRoles(api huma.API, deps APIDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "list-roles", Method: http.MethodGet, Path: "/roles",
		Summary: "List roles", Tags: []string{"roles"},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Roles []RoleDTO `json:"roles"`
		}
	}, error) {
		list, err := deps.Store.ListRoles()
		if err != nil {
			return nil, huma.Error500InternalServerError("list roles", err)
		}
		out := &struct {
			Body struct {
				Roles []RoleDTO `json:"roles"`
			}
		}{}
		for _, r := range list {
			out.Body.Roles = append(out.Body.Roles, toRoleDTO(r))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "create-role", Method: http.MethodPost, Path: "/roles",
		Summary: "Create a role", Tags: []string{"roles"}, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct {
		Body struct {
			Name            string `json:"name"`
			DefaultConfigID *int64 `json:"defaultConfigId,omitempty"`
		}
	}) (*struct{ Body RoleDTO }, error) {
		if in.Body.Name == "" {
			return nil, huma.Error422UnprocessableEntity("name is required")
		}
		id, err := deps.Store.CreateRole(in.Body.Name, in.Body.DefaultConfigID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("create role (duplicate name?)", err)
		}
		return roleDTOResp(deps.Store, id)
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-role", Method: http.MethodPut, Path: "/roles/{id}",
		Summary: "Update a role", Tags: []string{"roles"},
	}, func(ctx context.Context, in *struct {
		ID   int64 `path:"id"`
		Body struct {
			Name            *string `json:"name,omitempty"`
			DefaultConfigID *int64  `json:"defaultConfigId,omitempty"`
		}
	}) (*struct{ Body RoleDTO }, error) {
		if _, err := deps.Store.GetRole(in.ID); errors.Is(err, db.ErrNotFound) {
			return nil, huma.Error404NotFound("role not found")
		} else if err != nil {
			return nil, huma.Error500InternalServerError("get role", err)
		}
		if in.Body.Name != nil && *in.Body.Name == "" {
			return nil, huma.Error422UnprocessableEntity("name cannot be empty")
		}
		if err := deps.Store.UpdateRole(in.ID, in.Body.Name, in.Body.DefaultConfigID); err != nil {
			return nil, huma.Error500InternalServerError("update role", err)
		}
		return roleDTOResp(deps.Store, in.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID: "delete-role", Method: http.MethodDelete, Path: "/roles/{id}",
		Summary: "Delete a role (disabled until auth)", Tags: []string{"roles"},
	}, func(ctx context.Context, _ *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, huma.Error403Forbidden("destructive endpoints are disabled until authentication lands (P10)")
	})
}

func roleDTOResp(store *db.Store, id int64) (*struct{ Body RoleDTO }, error) {
	list, err := store.ListRoles()
	if err != nil {
		return nil, huma.Error500InternalServerError("read back role", err)
	}
	for _, r := range list {
		if r.ID == id {
			return &struct{ Body RoleDTO }{Body: toRoleDTO(r)}, nil
		}
	}
	return nil, huma.Error500InternalServerError("role vanished after write", errors.New("not found"))
}
