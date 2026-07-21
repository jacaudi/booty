package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/ostype"
)

// FamilyDTO is the wire representation of an ostype.Family.
type FamilyDTO struct {
	Name           string   `json:"name"`
	ConfigKind     string   `json:"configKind"`
	AuthoringKinds []string `json:"authoringKinds"`
}

// OSDTO is the wire representation of an ostype.OS.
type OSDTO struct {
	Name           string   `json:"name"`
	Family         string   `json:"family"`
	RequiredParams []string `json:"requiredParams"`
}

type familiesOutput struct {
	Body struct {
		Families []FamilyDTO `json:"families"`
	}
}

type osOutput struct {
	Body struct {
		OS []OSDTO `json:"os"`
	}
}

// registerCatalog wires the open (read-only) catalog endpoints.
func registerCatalog(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-os", Method: http.MethodGet, Path: "/os",
		Summary: "List supported operating systems", Tags: []string{"catalog"},
	}, func(ctx context.Context, _ *struct{}) (*osOutput, error) {
		out := &osOutput{}
		for _, o := range ostype.All() {
			rp := o.RequiredParams()
			if rp == nil {
				rp = []string{}
			}
			out.Body.OS = append(out.Body.OS, OSDTO{
				Name: o.Name(), Family: o.Family().Name, RequiredParams: rp,
			})
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-families", Method: http.MethodGet, Path: "/families",
		Summary: "List boot-config families", Tags: []string{"catalog"},
	}, func(ctx context.Context, _ *struct{}) (*familiesOutput, error) {
		out := &familiesOutput{}
		seen := map[string]bool{}
		for _, o := range ostype.All() {
			f := o.Family()
			if seen[f.Name] {
				continue
			}
			seen[f.Name] = true
			out.Body.Families = append(out.Body.Families, FamilyDTO{
				Name: f.Name, ConfigKind: f.ConfigKind, AuthoringKinds: authoringKindsForFamily(f.ConfigKind),
			})
		}
		return out, nil
	})
}
