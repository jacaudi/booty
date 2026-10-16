package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeefy/booty/pkg/hardware"
)

// registerDiscovered attaches the routes that dismiss discovered machines.
// They delete discovered_hosts rows only and never touch the hosts table.
func registerDiscovered(api huma.API, deps APIDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "dismiss-discovered-host", Method: http.MethodDelete, Path: "/discovered/{mac}",
		Summary: "Dismiss a discovered host", Tags: []string{"hosts"},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *struct {
		MAC string `path:"mac"`
	}) (*struct{}, error) {
		mac, err := hardware.NormalizeMAC(in.MAC)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		}
		deleted, err := deps.Store.DeleteDiscoveredHost(ctx, mac)
		if err != nil {
			return nil, huma.Error500InternalServerError("dismiss discovered host", err)
		}
		if !deleted {
			return nil, huma.Error404NotFound("discovered host not found")
		}
		return nil, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "dismiss-all-discovered-hosts", Method: http.MethodDelete, Path: "/discovered",
		Summary: "Dismiss every discovered host", Tags: []string{"hosts"},
	}, func(ctx context.Context, _ *struct{}) (*dismissAllOutput, error) {
		n, err := deps.Store.DeleteAllDiscoveredHosts(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("dismiss discovered hosts", err)
		}
		out := &dismissAllOutput{}
		out.Body.Deleted = n
		return out, nil
	})
}

type dismissAllOutput struct {
	Body struct {
		Deleted int64 `json:"deleted"`
	}
}
