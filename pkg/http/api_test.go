package http

import (
	"testing"

	"github.com/danielgtaylor/huma/v2/humatest"
)

// newTestAPI builds a Huma API over a humatest router with the given deps so
// each group's handlers can be exercised without a live server.
func newTestAPI(t *testing.T, deps APIDeps) humatest.TestAPI {
	t.Helper()
	_, api := humatest.New(t)
	registerOperations(api, deps) // group-registration entrypoint (Task 5-7 fill it)
	return api
}

func TestAPIScaffoldServesOpenAPI(t *testing.T) {
	api := newTestAPI(t, APIDeps{})
	// With no operations yet this still must not panic; the openapi doc is served
	// by Huma itself. A trivial GET to a missing route returns 404, proving the
	// adapter is wired.
	resp := api.Get("/api/v1/does-not-exist")
	if resp.Code != 404 {
		t.Fatalf("unmounted route = %d, want 404", resp.Code)
	}
}
