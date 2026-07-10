package http

import (
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// clustersTestSetup = the host/store fixture + a live --secretsKey + a factory
// URL, so cluster-secret operations are enabled. Reused by the members suite.
// hostsTestSetup does not touch viper, so reset it up front (no stale
// SecretsKey/DataDir from a prior test) and again on cleanup.
func clustersTestSetup(t *testing.T) APIDeps {
	viper.Reset()
	t.Cleanup(viper.Reset)
	deps := hostsTestSetup(t)
	testSecretsKey(t)
	viper.Set(config.TalosFactoryURL, "https://factory.talos.dev")
	return deps
}

func TestCreateClusterMintsEncryptedBundle(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)

	resp := api.Post("/api/v1/clusters", map[string]any{
		"name": "prod", "endpoint": "https://10.0.0.10:6443",
		"talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	})
	if resp.Code != 201 {
		t.Fatalf("create cluster = %d: %s", resp.Code, resp.Body.String())
	}
	// The stored secrets are ciphertext, and non-empty.
	c, err := deps.Store.GetCluster(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.SecretsEnc) == 0 {
		t.Fatal("secrets bundle not stored")
	}
	// It must decrypt back to a usable bundle (regenerates a config).
	raw, err := decryptSecrets(c.SecretsEnc)
	if err != nil {
		t.Fatalf("stored bundle does not decrypt: %v", err)
	}
	if _, err := unmarshalBundle(raw); err != nil {
		t.Fatalf("stored bundle does not unmarshal: %v", err)
	}
}

func TestCreateClusterFailClosedWithoutKey(t *testing.T) {
	deps := hostsTestSetup(t)
	viper.Set(config.TalosFactoryURL, "https://factory.talos.dev")
	t.Cleanup(viper.Reset)
	api := newTestAPI(t, deps)

	resp := api.Post("/api/v1/clusters", map[string]any{
		"name": "nope", "endpoint": "https://e:6443",
		"talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	})
	if resp.Code != 422 {
		t.Fatalf("create without --secretsKey = %d, want 422 (fail-closed)", resp.Code)
	}
}

func TestClusterCreateValidatesInputs(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	for _, tc := range []map[string]any{
		{"name": "", "endpoint": "https://e:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0"},
		{"name": "badver", "endpoint": "https://e:6443", "talosVersion": "not-a-version", "k8sVersion": "v1.34.0"},
	} {
		if resp := api.Post("/api/v1/clusters", tc); resp.Code != 422 {
			t.Fatalf("invalid create %v = %d, want 422", tc, resp.Code)
		}
	}
}

func TestListGetUpdateCluster(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	if resp := api.Post("/api/v1/clusters", map[string]any{
		"name": "c", "endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	}); resp.Code != 201 {
		t.Fatalf("create = %d: %s", resp.Code, resp.Body.String())
	}

	if resp := api.Get("/api/v1/clusters"); resp.Code != 200 {
		t.Fatalf("list = %d", resp.Code)
	}
	if resp := api.Get("/api/v1/clusters/1"); resp.Code != 200 {
		t.Fatalf("get = %d: %s", resp.Code, resp.Body.String())
	}
	if resp := api.Get("/api/v1/clusters/999"); resp.Code != 404 {
		t.Fatalf("get missing = %d, want 404", resp.Code)
	}

	up := api.Put("/api/v1/clusters/1", map[string]any{
		"endpoint": "https://10.0.0.99:6443", "talosVersion": "v1.13.9", "k8sVersion": "v1.35.0",
	})
	if up.Code != 200 {
		t.Fatalf("update = %d: %s", up.Code, up.Body.String())
	}
	c, _ := deps.Store.GetCluster(1)
	if c.Endpoint != "https://10.0.0.99:6443" || c.TalosVersion != "v1.13.9" {
		t.Fatalf("update not applied: %+v", c)
	}
}

// TestUpdateClusterVersionBumpPreCaches (Important #1): a PUT that bumps
// talos_version must ensure the new version's cache targets for every member
// and Trigger a reconcile, so a member rebooting before re-bind can netboot the
// newly-pinned kernel instead of 404ing (the I1 live-pin vs deferred-freeze
// desync). A PUT that does NOT change the version does neither.
func TestUpdateClusterVersionBumpPreCaches(t *testing.T) {
	deps := clustersTestSetup(t)
	triggered := 0
	deps.Trigger = func() { triggered++ }
	api := newTestAPI(t, deps)

	api.Post("/api/v1/clusters", map[string]any{
		"name": "vb", "endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	})
	// A member with an explicit schematic, wired via the store (add-member is a
	// T14 endpoint, stubbed here).
	const mac = "aa:bb:cc:dd:ee:d0"
	if err := deps.Store.UpsertHost(db.Host{MAC: mac, OS: "talos", Schematic: "schemvb"}); err != nil {
		t.Fatal(err)
	}
	if err := deps.Store.SetHostCluster(mac, ptr(int64(1))); err != nil {
		t.Fatal(err)
	}

	// Same-version PUT: no caching side effects.
	if resp := api.Put("/api/v1/clusters/1", map[string]any{
		"endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	}); resp.Code != 200 {
		t.Fatalf("same-version update = %d: %s", resp.Code, resp.Body.String())
	}
	if triggered != 0 {
		t.Fatalf("same-version PUT triggered caching (%d)", triggered)
	}

	// Version-bump PUT: ensures the member's cache target + triggers a reconcile.
	if resp := api.Put("/api/v1/clusters/1", map[string]any{
		"endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.9", "k8sVersion": "v1.34.0",
	}); resp.Code != 200 {
		t.Fatalf("version-bump update = %d: %s", resp.Code, resp.Body.String())
	}
	if triggered != 1 {
		t.Fatalf("version-bump PUT trigger count = %d, want 1", triggered)
	}
	targets, _ := deps.Store.ListTargets()
	found := false
	for _, tg := range targets {
		if tg.OS == "talos" && strings.Contains(tg.Params, "schemvb") {
			found = true
		}
	}
	if !found {
		t.Fatal("version bump did not ensure the member's (schematic) cache target")
	}
}

// ptr is a tiny test helper for *int64 literals.
func ptr(v int64) *int64 { return &v }

func TestExportClusterSecrets(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/clusters", map[string]any{
		"name": "exp", "endpoint": "https://e:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	})
	resp := api.Post("/api/v1/clusters/1/export", map[string]any{})
	if resp.Code != 200 {
		t.Fatalf("export = %d: %s", resp.Code, resp.Body.String())
	}
	// The exported secrets.yaml is the plaintext bundle (talosctl-compatible).
	// go.yaml.in/yaml/v4 lowercases the Bundle's field names, so match "certs".
	if !strings.Contains(strings.ToLower(resp.Body.String()), "certs") {
		t.Fatalf("export does not look like a secrets bundle: %s", resp.Body.String())
	}
}

func TestListClustersEmptyIsArray(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	// With no clusters, the list field must be [] not null.
	resp := api.Get("/api/v1/clusters")
	if resp.Code != 200 {
		t.Fatalf("list = %d: %s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), `"clusters":null`) {
		t.Fatalf("empty list serialized clusters as null: %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"clusters":[]`) {
		t.Fatalf("empty list should serialize clusters as []: %s", resp.Body.String())
	}
}

func TestClusterDTOMembersNeverNull(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	// A memberless cluster must serialize members as [] (a list field), never
	// null — a null crashes list-field consumers (the web view calls .length).
	resp := api.Post("/api/v1/clusters", map[string]any{
		"name": "empty", "endpoint": "https://e:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	})
	if resp.Code != 201 {
		t.Fatalf("create = %d: %s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), `"members":null`) {
		t.Fatalf("memberless cluster serialized members as null: %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"members":[]`) {
		t.Fatalf("memberless cluster should serialize members as []: %s", resp.Body.String())
	}
	// And the list endpoint too.
	list := api.Get("/api/v1/clusters")
	if strings.Contains(list.Body.String(), `"members":null`) {
		t.Fatalf("list serialized members as null: %s", list.Body.String())
	}
}

func TestDeleteClusterForbidden(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/clusters", map[string]any{
		"name": "del", "endpoint": "https://e:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	})
	if resp := api.Delete("/api/v1/clusters/1"); resp.Code != 403 {
		t.Fatalf("delete = %d, want 403 (until P10)", resp.Code)
	}
}
