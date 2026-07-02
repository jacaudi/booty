package http

import (
	"path/filepath"
	"strings"
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

func TestCreateTargetUnsafeParamIs422(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	cases := []map[string]any{
		{"os": "flatcar", "arch": "amd64", "params": map[string]string{"channel": "../evil"}, "mode": "discovery", "retainN": 1},
		{"os": "flatcar", "arch": "amd64", "params": map[string]string{"channel": "a/b"}, "mode": "discovery", "retainN": 1},
		{"os": "talos", "arch": "amd64", "params": map[string]string{"schematic": "..%2f"}, "mode": "discovery", "retainN": 1},
	}
	for _, body := range cases {
		if resp := api.Post("/api/v1/targets", body); resp.Code != 422 {
			t.Errorf("POST %v = %d, want 422 (param becomes a path segment)", body, resp.Code)
		}
	}
}

func TestCreateTargetUnexpectedParamIs422(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	// An unrequested key must be rejected: paramSegment gives "schematic"
	// precedence over "channel", so an extra schematic on a flatcar target
	// would become an UNVALIDATED path segment (traversal at reconcile time).
	resp := api.Post("/api/v1/targets", map[string]any{
		"os": "flatcar", "arch": "amd64",
		"params": map[string]string{"channel": "beta", "schematic": "../../../etc/pwned"},
		"mode": "discovery", "retainN": 1,
	})
	if resp.Code != 422 {
		t.Fatalf("unexpected param key = %d, want 422", resp.Code)
	}
}

func TestCreateFlatcarTargetRequiresChannel(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	// #48: flatcar/fedora-coreos are params-driven; channel is required.
	if resp := api.Post("/api/v1/targets", map[string]any{
		"os": "flatcar", "arch": "amd64", "mode": "discovery", "retainN": 1,
	}); resp.Code != 422 {
		t.Errorf("flatcar without channel = %d, want 422", resp.Code)
	}
	resp := api.Post("/api/v1/targets", map[string]any{
		"os": "flatcar", "arch": "amd64", "params": map[string]string{"channel": "beta"},
		"mode": "discovery", "retainN": 2,
	})
	if resp.Code != 201 || !strings.Contains(resp.Body.String(), `"channel":"beta"`) {
		t.Errorf("flatcar with channel = %d: %s", resp.Code, resp.Body.String())
	}
}
