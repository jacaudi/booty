package http

import (
	"strings"
	"testing"
)

func TestRolesCRUD(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)

	if resp := api.Post("/api/v1/roles", map[string]any{"name": "controlplane"}); resp.Code != 201 {
		t.Fatalf("create = %d: %s", resp.Code, resp.Body.String())
	}
	if resp := api.Get("/api/v1/roles"); resp.Code != 200 || !strings.Contains(resp.Body.String(), "controlplane") {
		t.Fatalf("list = %d: %s", resp.Code, resp.Body.String())
	}
	if resp := api.Put("/api/v1/roles/1", map[string]any{"name": "cp"}); resp.Code != 200 {
		t.Fatalf("update = %d: %s", resp.Code, resp.Body.String())
	}
	if resp := api.Delete("/api/v1/roles/1"); resp.Code != 403 {
		t.Fatalf("delete = %d, want 403", resp.Code)
	}
}

func TestRoleCreateEmptyNameIs422(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	if resp := api.Post("/api/v1/roles", map[string]any{"name": ""}); resp.Code != 422 {
		t.Fatalf("empty name = %d, want 422", resp.Code)
	}
}

func TestRoleUpdateNotFoundIs404(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	if resp := api.Put("/api/v1/roles/999", map[string]any{"name": "x"}); resp.Code != 404 {
		t.Fatalf("missing role = %d, want 404", resp.Code)
	}
}
