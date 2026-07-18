package http

import (
	"fmt"
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

func TestCreateTargetUnsafeArchIs422(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	resp := api.Post("/api/v1/targets", map[string]any{
		"os": "fedora-coreos", "arch": "../../../../tmp/x",
		"params": map[string]string{"channel": "stable"},
		"mode":   "discovery", "retainN": 1,
	})
	if resp.Code != 422 {
		t.Fatalf("traversal arch = %d, want 422 (arch becomes a path segment)", resp.Code)
	}
	resp = api.Post("/api/v1/targets", map[string]any{
		"os": "fedora-coreos", "arch": "x86_64",
		"params": map[string]string{"channel": "stable"},
		"mode":   "discovery", "retainN": 1,
	})
	if resp.Code != 201 {
		t.Fatalf("valid arch x86_64 = %d, want 201: %s", resp.Code, resp.Body.String())
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

// TestGetTargetDTOIncludesModeState covers toTargetDTO surfacing the mode
// state (source_mode/dvd_count/desired_mode) added for the promote-dvd
// endpoint (I3), so operators can see a target's serving mode and any
// pending promote via GET /targets/{id}.
func TestGetTargetDTOIncludesModeState(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	id, err := deps.Store.CreateTarget(db.Target{
		OS: "debian", Arch: "amd64", Params: `{"channel":"12"}`,
		Mode: "discovery", RetainN: 1, Source: "api", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	resp := api.Get(fmt.Sprintf("/api/v1/targets/%d", id))
	if resp.Code != 200 {
		t.Fatalf("GET /targets/%d = %d: %s", id, resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"sourceMode":"netinst"`) {
		t.Errorf("body missing sourceMode=netinst: %s", body)
	}
	if !strings.Contains(body, `"dvdCount":1`) {
		t.Errorf("body missing dvdCount=1: %s", body)
	}
	if !strings.Contains(body, `"desiredMode":""`) {
		t.Errorf("body missing desiredMode empty: %s", body)
	}
}

func TestPromoteDVD_HappyPath(t *testing.T) {
	deps, calls := targetsTestDeps(t) // existing harness (api_targets_test.go:11)
	api := newTestAPI(t, deps)
	id, _ := deps.Store.CreateTarget(db.Target{OS: "debian", Arch: "amd64", Params: `{"channel":"12"}`,
		Mode: "discovery", RetainN: 1, Source: "api", Enabled: true, SourceMode: "netinst", DvdCount: 1})
	resp := api.Post(fmt.Sprintf("/api/v1/targets/%d/promote-dvd", id), map[string]any{"dvdCount": 3})
	if resp.Code != 200 {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	got, _ := deps.Store.GetTarget(id)
	if got.DesiredMode != "dvd" || got.DvdCount != 3 {
		t.Fatalf("desired=%q dvd_count=%d", got.DesiredMode, got.DvdCount)
	}
	if *calls != 1 {
		t.Fatalf("promote must Trigger() a reconcile once, got %d", *calls)
	}
}

func TestPromoteDVD_RejectsArm64NonDebianAndAlreadyDVD(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	mk := func(tg db.Target) int64 { id, _ := deps.Store.CreateTarget(tg); return id }

	arm := mk(db.Target{OS: "debian", Arch: "arm64", Params: `{"channel":"13"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true, SourceMode: "netinst"})
	if api.Post(fmt.Sprintf("/api/v1/targets/%d/promote-dvd", arm), map[string]any{}).Code == 200 {
		t.Fatal("arm64 must be rejected (DVDs are amd64-only)")
	}
	flat := mk(db.Target{OS: "flatcar", Arch: "amd64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
	if api.Post(fmt.Sprintf("/api/v1/targets/%d/promote-dvd", flat), map[string]any{}).Code == 200 {
		t.Fatal("non-debian must be rejected")
	}
	dvd := mk(db.Target{OS: "debian", Arch: "amd64", Params: `{"channel":"12"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true, SourceMode: "dvd"})
	if api.Post(fmt.Sprintf("/api/v1/targets/%d/promote-dvd", dvd), map[string]any{}).Code == 200 {
		t.Fatal("already-dvd must be rejected (409)")
	}
}
