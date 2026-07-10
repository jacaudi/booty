package http

import (
	"encoding/base64"
	"testing"

	"github.com/jeefy/booty/pkg/hardware"
)

func TestSchematicBindByConfigAndFreeEntry(t *testing.T) {
	deps := hostsTestSetup(t)
	api := newTestAPI(t, deps)
	const mac = "aa:bb:cc:00:00:60"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"}); err != nil {
		t.Fatal(err)
	}

	// Registry bind: the config's CURRENT active derived ID lands on the host.
	cid, _ := deps.Store.CreateConfig("cp-min", "schematic")
	id := "a1b2c3d4"
	rid, _, _ := deps.Store.AddConfigRevision(cid, base64.StdEncoding.EncodeToString([]byte("customization: {}\n")), "sha", &id)
	deps.Store.SetActiveRevision(cid, rid)
	if resp := api.Post("/api/v1/hosts/"+mac+"/schematic", map[string]any{"configId": cid}); resp.Code != 200 {
		t.Fatalf("bind by config = %d: %s", resp.Code, resp.Body.String())
	}
	if h, _ := hardware.GetMacAddress(mac); h.Schematic != id {
		t.Fatalf("host schematic = %q, want %q", h.Schematic, id)
	}

	// Free entry (advisory registry — raw IDs still work).
	if resp := api.Post("/api/v1/hosts/"+mac+"/schematic", map[string]any{"schematic": "feedface"}); resp.Code != 200 {
		t.Fatalf("free-entry bind = %d: %s", resp.Code, resp.Body.String())
	}
	if h, _ := hardware.GetMacAddress(mac); h.Schematic != "feedface" {
		t.Fatalf("free-entry schematic = %q, want feedface", h.Schematic)
	}
}

func TestSchematicBindValidation(t *testing.T) {
	deps := hostsTestSetup(t)
	api := newTestAPI(t, deps)
	const talosMAC = "aa:bb:cc:00:00:61"
	const flatcarMAC = "aa:bb:cc:00:00:62"
	hardware.WriteMacAddress(talosMAC, hardware.Host{MAC: talosMAC, OS: "talos"})
	hardware.WriteMacAddress(flatcarMAC, hardware.Host{MAC: flatcarMAC, OS: "flatcar"})

	// Exactly one of configId | schematic.
	if resp := api.Post("/api/v1/hosts/"+talosMAC+"/schematic", map[string]any{}); resp.Code != 422 {
		t.Fatalf("neither field = %d, want 422", resp.Code)
	}
	if resp := api.Post("/api/v1/hosts/"+talosMAC+"/schematic", map[string]any{"configId": 1, "schematic": "x1"}); resp.Code != 422 {
		t.Fatalf("both fields = %d, want 422", resp.Code)
	}
	// Talos hosts only (mirrors the approve path's h.OS == "talos").
	if resp := api.Post("/api/v1/hosts/"+flatcarMAC+"/schematic", map[string]any{"schematic": "a1b2c3d4"}); resp.Code != 422 {
		t.Fatalf("non-talos host = %d, want 422", resp.Code)
	}
	// Unknown host → 404.
	if resp := api.Post("/api/v1/hosts/aa:bb:cc:00:00:ff/schematic", map[string]any{"schematic": "a1b2c3d4"}); resp.Code != 404 {
		t.Fatalf("unknown host = %d, want 404", resp.Code)
	}
	// A non-schematic config cannot be bound as a schematic.
	bid, _ := deps.Store.CreateConfig("butane-cfg", "butane")
	if resp := api.Post("/api/v1/hosts/"+talosMAC+"/schematic", map[string]any{"configId": bid}); resp.Code != 422 {
		t.Fatalf("butane config = %d, want 422", resp.Code)
	}
	// A schematic config with no built revision cannot be bound.
	sid, _ := deps.Store.CreateConfig("unbuilt", "schematic")
	if resp := api.Post("/api/v1/hosts/"+talosMAC+"/schematic", map[string]any{"configId": sid}); resp.Code != 422 {
		t.Fatalf("unbuilt schematic config = %d, want 422", resp.Code)
	}
	// Missing config → 422 (matches the /bind phrasing).
	if resp := api.Post("/api/v1/hosts/"+talosMAC+"/schematic", map[string]any{"configId": 999}); resp.Code != 422 {
		t.Fatalf("missing config = %d, want 422", resp.Code)
	}
	// Path-unsafe free entry is rejected (the value becomes a cache segment).
	if resp := api.Post("/api/v1/hosts/"+talosMAC+"/schematic", map[string]any{"schematic": "../evil"}); resp.Code != 422 {
		t.Fatalf("unsafe free entry = %d, want 422", resp.Code)
	}
	// Nothing above may have written a schematic.
	if h, _ := hardware.GetMacAddress(talosMAC); h.Schematic != "" {
		t.Fatalf("failed binds must not mutate the host, schematic = %q", h.Schematic)
	}
}

// TestSchematicBindApproveParamsByteIdentical is the acceptance-criterion #4
// guard: after a P5 bind, approve must produce EXACTLY the target-param
// encoding it produces today for a host whose Schematic was written directly
// (the pre-P5 path). If this drifts by one byte, the cache key — and thus the
// on-disk/URL layout — silently forks.
func TestSchematicBindApproveParamsByteIdentical(t *testing.T) {
	deps := hostsTestSetup(t)
	api := newTestAPI(t, deps)
	const id = "a1b2c3d4"

	// Control: pre-P5 — schematic written directly on the host row.
	const legacy = "aa:bb:cc:00:00:63"
	if err := hardware.WriteMacAddress(legacy, hardware.Host{MAC: legacy, OS: "talos", Schematic: id}); err != nil {
		t.Fatal(err)
	}
	if resp := api.Post("/api/v1/hosts/"+legacy+"/approve", map[string]any{}); resp.Code != 200 {
		t.Fatalf("legacy approve = %d", resp.Code)
	}

	// P5: the same ID arrives via the bind endpoint, then approve.
	const bound = "aa:bb:cc:00:00:64"
	if err := hardware.WriteMacAddress(bound, hardware.Host{MAC: bound, OS: "talos"}); err != nil {
		t.Fatal(err)
	}
	if resp := api.Post("/api/v1/hosts/"+bound+"/schematic", map[string]any{"schematic": id}); resp.Code != 200 {
		t.Fatalf("bind = %d", resp.Code)
	}
	if resp := api.Post("/api/v1/hosts/"+bound+"/approve", map[string]any{}); resp.Code != 200 {
		t.Fatalf("bound approve = %d", resp.Code)
	}

	lh, _ := hardware.GetMacAddress(legacy)
	bh, _ := hardware.GetMacAddress(bound)
	const want = `{"schematic":"a1b2c3d4"}` // cache.EncodeParams canonical form
	if lh.AssignedParams != want {
		t.Fatalf("legacy params = %q, want %q (pre-P5 baseline broke)", lh.AssignedParams, want)
	}
	if bh.AssignedParams != want {
		t.Fatalf("bound params = %q, want %q (P5 perturbed the boot path)", bh.AssignedParams, want)
	}
}
