package http

import (
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/db"
)

func targetsTestDeps(t *testing.T) (APIDeps, *int) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	calls := 0
	return APIDeps{Store: store, Trigger: func() { calls++ }}, &calls
}

func TestCreateTargetTriggersReconcile(t *testing.T) {
	deps, calls := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	resp := api.Post("/api/v1/targets", map[string]any{
		"os": "talos", "arch": "amd64", "params": map[string]string{"schematic": "abc"},
		"mode": "discovery", "retainN": 3,
	})
	if resp.Code != 201 {
		t.Fatalf("POST /targets = %d: %s", resp.Code, resp.Body.String())
	}
	if *calls != 1 {
		t.Fatalf("Trigger called %d times, want 1", *calls)
	}
}

func TestCreateTargetUnknownOSIs422(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	resp := api.Post("/api/v1/targets", map[string]any{
		"os": "plan9", "arch": "amd64", "mode": "discovery", "retainN": 1,
	})
	if resp.Code != 422 {
		t.Fatalf("unknown OS = %d, want 422", resp.Code)
	}
}

func TestCreateTargetMissingRequiredParamIs422(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	// talos requires "schematic"
	resp := api.Post("/api/v1/targets", map[string]any{
		"os": "talos", "arch": "amd64", "mode": "discovery", "retainN": 1,
	})
	if resp.Code != 422 {
		t.Fatalf("missing schematic = %d, want 422", resp.Code)
	}
}

func TestDeleteTargetIs403UntilAuth(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	resp := api.Delete("/api/v1/targets/1")
	if resp.Code != 403 {
		t.Fatalf("DELETE /targets/1 = %d, want 403 (wired-but-disabled)", resp.Code)
	}
}
