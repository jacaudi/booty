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

// TestCreateTargetRejectsWrongArchForOS covers the API create path enforcing
// the same os/arch rule the catalog does (Task 5): a memtest86plus/arm64
// target would 404 on every download since memtest86plus is amd64-only.
func TestCreateTargetRejectsWrongArchForOS(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	resp := api.Post("/api/v1/targets", map[string]any{
		"os": "memtest86plus", "arch": "arm64", "mode": "discovery", "retainN": 1,
	})
	if resp.Code != 422 {
		t.Fatalf("memtest86plus/arm64 = %d, want 422: %s", resp.Code, resp.Body.String())
	}
	// Control: amd64 must be accepted, proving the gate rejects on arch and
	// not on everything.
	resp = api.Post("/api/v1/targets", map[string]any{
		"os": "memtest86plus", "arch": "amd64", "mode": "discovery", "retainN": 1,
	})
	if resp.Code != 201 {
		t.Fatalf("memtest86plus/amd64 = %d, want 201: %s", resp.Code, resp.Body.String())
	}
}

// TestCreateTargetRejectsBadRetainForTool closes the gap validateCatalog
// already closes (a tool with retain != 1 is rejected): the API create path
// must enforce the same rule, or an over-retained tool sits as
// desired-but-uncachable state forever (Artifacts refuses any version that
// isn't upstream's current tag, so every reconcile tick logs "artifacts
// unavailable; skipping version this tick").
func TestCreateTargetRejectsBadRetainForTool(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	resp := api.Post("/api/v1/targets", map[string]any{
		"os": "memtest86plus", "arch": "amd64", "mode": "discovery", "retainN": 3,
	})
	if resp.Code != 422 {
		t.Fatalf("memtest86plus retainN=3 = %d, want 422: %s", resp.Code, resp.Body.String())
	}
	// Control: a DIFFERENT tool with retainN=1 must be accepted, proving the
	// gate rejects on retain and not on everything. targets has a UNIQUE
	// constraint on (os, arch, params) — reusing memtest86plus here would 422
	// on duplicate and look like the gate working when it is not.
	resp = api.Post("/api/v1/targets", map[string]any{
		"os": "systemrescue", "arch": "amd64", "mode": "discovery", "retainN": 1,
	})
	if resp.Code != 201 {
		t.Fatalf("systemrescue retainN=1 = %d, want 201: %s", resp.Code, resp.Body.String())
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

// PATCH is the second write path onto a target's RetainN, and until this test
// it validated nothing: POST carries `minimum:"0"` AND calls ValidateToolRetain,
// PATCH did neither. A negative value persisted here reaches retentionFor's
// `out[:n]` on the next reconcile pass and panics the process — HTTP 200 first,
// then the server is gone. Catalog-managed targets self-heal because
// applyCatalog reasserts retain every pass; source=api targets do not.
func TestPatchTargetRejectsNegativeRetainN(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	create := api.Post("/api/v1/targets", map[string]any{
		"os": "talos", "arch": "amd64", "params": map[string]string{"schematic": "abc"},
		"mode": "discovery", "retainN": 3,
	})
	if create.Code != 201 {
		t.Fatalf("POST /targets = %d: %s", create.Code, create.Body.String())
	}
	resp := api.Patch("/api/v1/targets/1", map[string]any{"retainN": -5})
	if resp.Code != 422 {
		t.Fatalf("PATCH retainN=-5 = %d, want 422: %s", resp.Code, resp.Body.String())
	}
}

// The tool-retain rule must hold on PATCH for the same reason ValidateOSArch
// and ValidateToolRetain were single-sourced across the catalog and POST paths:
// a tool target at retain != 1 pins or drops releases incorrectly, and a second
// unguarded write path silently reopens the hole the POST gate closed.
// retainN 5 is >= 0, so huma's schema check passes and the handler-level
// ValidateToolRetain call is what this exercises.
func TestPatchTargetRejectsToolRetainNotOne(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	create := api.Post("/api/v1/targets", map[string]any{
		"os": "clonezilla", "arch": "amd64", "mode": "discovery", "retainN": 1,
	})
	if create.Code != 201 {
		t.Fatalf("POST /targets = %d: %s", create.Code, create.Body.String())
	}
	resp := api.Patch("/api/v1/targets/1", map[string]any{"retainN": 5})
	if resp.Code != 422 {
		t.Fatalf("PATCH tool retainN=5 = %d, want 422: %s", resp.Code, resp.Body.String())
	}
}

// The control: a legal PATCH must still succeed, so the two gates above cannot
// pass by rejecting everything.
func TestPatchTargetAcceptsValidRetainN(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	create := api.Post("/api/v1/targets", map[string]any{
		"os": "talos", "arch": "amd64", "params": map[string]string{"schematic": "abc"},
		"mode": "discovery", "retainN": 3,
	})
	if create.Code != 201 {
		t.Fatalf("POST /targets = %d: %s", create.Code, create.Body.String())
	}
	resp := api.Patch("/api/v1/targets/1", map[string]any{"retainN": 2})
	if resp.Code != 200 {
		t.Fatalf("PATCH retainN=2 = %d, want 200: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"retainN":2`) {
		t.Fatalf("retainN not applied: %s", resp.Body.String())
	}
}
