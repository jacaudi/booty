package http

import "testing"

func TestTalosClusterConfigCreateValidatesSpec(t *testing.T) {
	deps := hostsTestSetup(t)
	api := newTestAPI(t, deps)

	// A valid spec (parses + all patches load) is accepted.
	ok := api.Post("/api/v1/configs", map[string]any{
		"name": "cluster-spec", "kind": "taloscluster",
		"source": "clusterPatches:\n  - \"machine:\\n  network:\\n    hostname: x\\n\"\n",
	})
	if ok.Code != 201 {
		t.Fatalf("valid taloscluster spec = %d: %s", ok.Code, ok.Body.String())
	}

	// A spec with an unparseable patch is 422 — no config row written.
	bad := api.Post("/api/v1/configs", map[string]any{
		"name": "bad-spec", "kind": "taloscluster",
		"source": "clusterPatches:\n  - \"::: not yaml :::\"\n",
	})
	if bad.Code != 422 {
		t.Fatalf("bad taloscluster spec = %d, want 422", bad.Code)
	}
}

func TestTalosClusterConfigNeverRenderable(t *testing.T) {
	deps := hostsTestSetup(t)
	api := newTestAPI(t, deps)
	created := api.Post("/api/v1/configs", map[string]any{
		"name": "spec2", "kind": "taloscluster", "source": "clusterPatches: []\n",
	})
	if created.Code != 201 {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	// Preview must 422 — a taloscluster config is not a template.
	prev := api.Post("/api/v1/configs/1/preview", map[string]any{})
	if prev.Code != 422 {
		t.Fatalf("taloscluster preview = %d, want 422", prev.Code)
	}
}
