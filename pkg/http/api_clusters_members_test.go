package http

import (
	"slices"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

func TestAddMemberGeneratesFreezesBinds(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/clusters", map[string]any{
		"name": "join", "endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	})
	const mac = "aa:bb:cc:dd:ee:c0"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"}); err != nil {
		t.Fatal(err)
	}

	resp := api.Post("/api/v1/clusters/1/members", map[string]any{
		"mac": mac, "machineType": "controlplane", "schematic": config.DefaultTalosSchematic,
	})
	if resp.Code != 200 {
		t.Fatalf("add member = %d: %s", resp.Code, resp.Body.String())
	}
	h, err := hardware.GetMacAddress(mac)
	if err != nil {
		t.Fatal(err)
	}
	if h.ClusterID == nil || *h.ClusterID != 1 || h.MachineType != "controlplane" ||
		h.NodeConfigID == nil || h.Schematic != config.DefaultTalosSchematic {
		t.Fatalf("member not fully bound: %+v", h)
	}
	// The frozen revision decrypts to a real machineconfig.
	nc, err := deps.Store.GetClusterNodeConfig(*h.NodeConfigID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decryptSecrets(nc.ConfigEnc)
	if err != nil {
		t.Fatalf("frozen config does not decrypt: %v", err)
	}
	if len(plain) == 0 || nc.Source != "generated" {
		t.Fatalf("frozen config wrong: source=%q len=%d", nc.Source, len(plain))
	}
}

// TestReBindExistingMemberMintsNewRevision (Important #2, AC6): re-binding an
// EXISTING same-cluster member after a version bump mints a NEW frozen revision
// (revision 2), advances the host's node_config_id to it, and the served bytes
// change — the exact "editing mints new frozen revisions; hosts roll forward on
// re-bind" behavior AC6 asserts.
func TestReBindExistingMemberMintsNewRevision(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/clusters", map[string]any{
		"name": "rebind", "endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	})
	const mac = "aa:bb:cc:dd:ee:c5"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"}); err != nil {
		t.Fatal(err)
	}
	// First bind → revision 1.
	if resp := api.Post("/api/v1/clusters/1/members", map[string]any{
		"mac": mac, "machineType": "controlplane", "schematic": config.DefaultTalosSchematic,
	}); resp.Code != 200 {
		t.Fatalf("first bind = %d: %s", resp.Code, resp.Body.String())
	}
	h1, _ := hardware.GetMacAddress(mac)
	nc1, _ := deps.Store.GetClusterNodeConfig(*h1.NodeConfigID)
	first, _ := decryptSecrets(nc1.ConfigEnc)

	// Bump the cluster version, then re-bind the SAME member.
	if resp := api.Put("/api/v1/clusters/1", map[string]any{
		"endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.9", "k8sVersion": "v1.34.0",
	}); resp.Code != 200 {
		t.Fatalf("version bump = %d: %s", resp.Code, resp.Body.String())
	}
	if resp := api.Post("/api/v1/clusters/1/members", map[string]any{
		"mac": mac, "machineType": "controlplane", "schematic": config.DefaultTalosSchematic,
	}); resp.Code != 200 {
		t.Fatalf("re-bind = %d: %s", resp.Code, resp.Body.String())
	}

	h2, _ := hardware.GetMacAddress(mac)
	if h2.NodeConfigID == nil || *h2.NodeConfigID == *h1.NodeConfigID {
		t.Fatalf("node_config_id did not advance on re-bind: was %v now %v", *h1.NodeConfigID, h2.NodeConfigID)
	}
	nc2, _ := deps.Store.GetClusterNodeConfig(*h2.NodeConfigID)
	if nc2.Revision != 2 {
		t.Fatalf("re-bind revision = %d, want 2", nc2.Revision)
	}
	second, _ := decryptSecrets(nc2.ConfigEnc)
	if string(second) == string(first) {
		t.Fatal("re-bound config bytes must change after a version bump")
	}
	if !bytesContains(second, "v1.13.9") {
		t.Fatal("re-bound config must carry the new pinned version in its install image")
	}
}

// TestReBindReusesPersistedHostPatch (Fold 3): a re-bind that OMITS a patch
// reuses the patch persisted on the current frozen revision, so the customization
// survives without being re-supplied (§1.1 durable-inputs).
func TestReBindReusesPersistedHostPatch(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/clusters", map[string]any{
		"name": "patchreuse", "endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	})
	const mac = "aa:bb:cc:dd:ee:c6"
	hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"})
	// First bind WITH a per-host patch.
	if resp := api.Post("/api/v1/clusters/1/members", map[string]any{
		"mac": mac, "machineType": "controlplane", "schematic": config.DefaultTalosSchematic,
		"patch": "machine:\n  nodeLabels:\n    role: patched-node\n",
	}); resp.Code != 200 {
		t.Fatalf("first bind = %d: %s", resp.Code, resp.Body.String())
	}
	h1, _ := hardware.GetMacAddress(mac)
	nc1, _ := deps.Store.GetClusterNodeConfig(*h1.NodeConfigID)
	if nc1.HostPatch == "" {
		t.Fatal("per-host patch not persisted on the revision")
	}
	first, _ := decryptSecrets(nc1.ConfigEnc)
	if !bytesContains(first, "patched-node") {
		t.Fatal("first config missing the patch effect")
	}

	// Re-bind OMITTING the patch → reused; hostname stays patched.
	if resp := api.Post("/api/v1/clusters/1/members", map[string]any{
		"mac": mac, "machineType": "controlplane", "schematic": config.DefaultTalosSchematic,
	}); resp.Code != 200 {
		t.Fatalf("re-bind = %d: %s", resp.Code, resp.Body.String())
	}
	h2, _ := hardware.GetMacAddress(mac)
	nc2, _ := deps.Store.GetClusterNodeConfig(*h2.NodeConfigID)
	if nc2.HostPatch != nc1.HostPatch {
		t.Fatalf("re-bind did not reuse persisted patch: %q vs %q", nc2.HostPatch, nc1.HostPatch)
	}
	second, _ := decryptSecrets(nc2.ConfigEnc)
	if !bytesContains(second, "patched-node") {
		t.Fatal("re-bind dropped the persisted per-host patch")
	}
}

// bytesContains is a tiny helper (avoids importing bytes for one call per test).
func bytesContains(b []byte, sub string) bool { return strings.Contains(string(b), sub) }

// talosManualVersions returns the manual-source pinned versions on the talos
// cache target whose schematic param equals `schematic`.
func talosManualVersions(t *testing.T, deps APIDeps, schematic string) []string {
	t.Helper()
	targets, err := deps.Store.ListTargets()
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if tg.OS == "talos" && strings.Contains(tg.Params, schematic) {
			vs, err := deps.Store.ListTargetVersions(tg.ID)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, v := range vs {
				if v.Source == "manual" {
					out = append(out, v.Version)
				}
			}
			return out
		}
	}
	return nil
}

// TestAddMemberPinsClusterVersionManually (I5): adding a member pins the
// cluster's talos_version as a MANUAL target version so the reconciler fetches
// it even when it is below the discovery window (closes the D-F back-fetch gap).
func TestAddMemberPinsClusterVersionManually(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/clusters", map[string]any{
		"name": "pinver", "endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.11.0", "k8sVersion": "v1.34.0",
	})
	const mac, schematic = "aa:bb:cc:dd:ee:e0", "schempin"
	hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"})
	if resp := api.Post("/api/v1/clusters/1/members", map[string]any{
		"mac": mac, "machineType": "controlplane", "schematic": schematic,
	}); resp.Code != 200 {
		t.Fatalf("add member = %d: %s", resp.Code, resp.Body.String())
	}
	if got := talosManualVersions(t, deps, schematic); !slices.Contains(got, "v1.11.0") {
		t.Fatalf("cluster version not pinned as manual: %v", got)
	}
}

// TestVersionBumpPinsNewVersionManually (I4+I5): a version-bump PUT pins the NEW
// version as a manual target version for every member (and, per I4, does so
// before committing the bump).
func TestVersionBumpPinsNewVersionManually(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/clusters", map[string]any{
		"name": "bumppin", "endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	})
	const mac, schematic = "aa:bb:cc:dd:ee:e1", "schembump"
	hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"})
	api.Post("/api/v1/clusters/1/members", map[string]any{"mac": mac, "machineType": "controlplane", "schematic": schematic})

	if resp := api.Put("/api/v1/clusters/1", map[string]any{
		"endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.9", "k8sVersion": "v1.34.0",
	}); resp.Code != 200 {
		t.Fatalf("version bump = %d: %s", resp.Code, resp.Body.String())
	}
	if got := talosManualVersions(t, deps, schematic); !slices.Contains(got, "v1.13.9") {
		t.Fatalf("bumped version not pinned as manual: %v", got)
	}
}

// TestVersionBumpPrecacheFailureDoesNotCommit (I4 atomicity): if the pre-cache
// step fails during a version-bump PUT, the version must NOT be advanced (it runs
// before UpdateCluster), so the live netboot pin stays put and the op is retryable.
func TestVersionBumpPrecacheFailureDoesNotCommit(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/clusters", map[string]any{
		"name": "atomic", "endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	})
	const mac = "aa:bb:cc:dd:ee:e4"
	hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"})
	api.Post("/api/v1/clusters/1/members", map[string]any{"mac": mac, "machineType": "controlplane", "schematic": "validschem"})
	// Corrupt the member's schematic to a non-path-safe value so the version-bump
	// pre-cache (EnsureSchematicTarget → ValidatePathParam) fails.
	if err := deps.Store.SetHostSchematic(mac, "bad/slash"); err != nil {
		t.Fatal(err)
	}
	if resp := api.Put("/api/v1/clusters/1", map[string]any{
		"endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.9", "k8sVersion": "v1.34.0",
	}); resp.Code == 200 {
		t.Fatalf("version bump with failing pre-cache = 200, want failure")
	}
	// The bump must NOT have committed — retryable.
	if c, _ := deps.Store.GetCluster(1); c.TalosVersion != "v1.13.5" {
		t.Fatalf("version advanced despite pre-cache failure: %s", c.TalosVersion)
	}
}

// TestReBindRejectsMachineTypeChange (M4): re-binding an existing member with a
// different machineType is refused (remove + re-add is the explicit path).
func TestReBindRejectsMachineTypeChange(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/clusters", map[string]any{
		"name": "mtchange", "endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0",
	})
	const mac = "aa:bb:cc:dd:ee:e2"
	hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"})
	if resp := api.Post("/api/v1/clusters/1/members", map[string]any{"mac": mac, "machineType": "controlplane"}); resp.Code != 200 {
		t.Fatalf("first bind = %d: %s", resp.Code, resp.Body.String())
	}
	// Re-bind with the SAME type is allowed (regenerate path).
	if resp := api.Post("/api/v1/clusters/1/members", map[string]any{"mac": mac, "machineType": "controlplane"}); resp.Code != 200 {
		t.Fatalf("same-type re-bind = %d: %s", resp.Code, resp.Body.String())
	}
	// Re-bind changing the type is refused.
	if resp := api.Post("/api/v1/clusters/1/members", map[string]any{"mac": mac, "machineType": "worker"}); resp.Code != 422 {
		t.Fatalf("machineType change on re-bind = %d, want 422", resp.Code)
	}
}

func TestAddMemberRejectsForeignClusterHost(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/clusters", map[string]any{"name": "a", "endpoint": "https://e:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0"})
	api.Post("/api/v1/clusters", map[string]any{"name": "b", "endpoint": "https://e:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0"})
	const mac = "aa:bb:cc:dd:ee:c1"
	hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"})
	// Bind to cluster 1.
	if resp := api.Post("/api/v1/clusters/1/members", map[string]any{"mac": mac, "machineType": "worker"}); resp.Code != 200 {
		t.Fatalf("first bind = %d: %s", resp.Code, resp.Body.String())
	}
	// Adding to cluster 2 must be refused (a host is in <=1 cluster).
	if resp := api.Post("/api/v1/clusters/2/members", map[string]any{"mac": mac, "machineType": "worker"}); resp.Code != 422 {
		t.Fatalf("second bind = %d, want 422", resp.Code)
	}
}

func TestRemoveMemberRevertsToP4(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/clusters", map[string]any{"name": "rm", "endpoint": "https://10.0.0.10:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0"})
	const mac = "aa:bb:cc:dd:ee:c2"
	hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"})
	api.Post("/api/v1/clusters/1/members", map[string]any{"mac": mac, "machineType": "worker", "schematic": config.DefaultTalosSchematic})

	if resp := api.Delete("/api/v1/clusters/1/members/" + mac); resp.Code != 200 && resp.Code != 204 {
		t.Fatalf("remove member = %d: %s", resp.Code, resp.Body.String())
	}
	h, _ := hardware.GetMacAddress(mac)
	if h.ClusterID != nil || h.MachineType != "" || h.NodeConfigID != nil {
		t.Fatalf("membership not cleared: %+v", h)
	}
	// Frozen revisions pruned.
	list, _ := deps.Store.ListClusterMembers(1)
	if len(list) != 0 {
		t.Fatalf("cluster still has members: %+v", list)
	}
}

func TestImportReconstructsCluster(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	cp := string(genControlPlaneBytes(t, "controlplane"))
	const mac = "aa:bb:cc:dd:ee:c3"
	hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"})

	resp := api.Post("/api/v1/clusters/import", map[string]any{
		"name":           "adopted",
		"controlplane":   cp,
		"controlplaneMac": mac,
	})
	if resp.Code != 201 {
		t.Fatalf("import = %d: %s", resp.Code, resp.Body.String())
	}
	c, err := deps.Store.GetCluster(1)
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint != "https://10.0.0.10:6443" || c.TalosVersion != "v1.13.5" || c.K8sVersion != "v1.34.0" {
		t.Fatalf("import fields wrong: %+v", c)
	}
	// The imported bytes are stored VERBATIM (source='imported') and byte-identical.
	h, _ := hardware.GetMacAddress(mac)
	if h.NodeConfigID == nil {
		t.Fatal("imported host not bound to a frozen config")
	}
	nc, _ := deps.Store.GetClusterNodeConfig(*h.NodeConfigID)
	plain, err := decryptSecrets(nc.ConfigEnc)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != cp || nc.Source != "imported" {
		t.Fatalf("imported bytes not verbatim: source=%q identical=%v", nc.Source, string(plain) == cp)
	}
}

func TestImportUnregisteredHostLeavesNoOrphanCluster(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	cp := string(genControlPlaneBytes(t, "controlplane"))
	// Control-plane MAC that booty does not know → import must fail AND must not
	// have committed a cluster row (name is UNIQUE + DELETE is 403 → orphan).
	resp := api.Post("/api/v1/clusters/import", map[string]any{
		"name": "orphan", "controlplane": cp, "controlplaneMac": "aa:bb:cc:dd:ee:f0",
	})
	if resp.Code == 201 {
		t.Fatalf("import with unregistered CP host unexpectedly succeeded")
	}
	list, err := deps.Store.ListClusters()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("failed import left an orphan cluster: %+v", list)
	}
}

func TestImportRejectsHostAlreadyInAnotherCluster(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	// Host bound to cluster A.
	api.Post("/api/v1/clusters", map[string]any{"name": "a", "endpoint": "https://e:6443", "talosVersion": "v1.13.5", "k8sVersion": "v1.34.0"})
	const mac = "aa:bb:cc:dd:ee:f1"
	hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"})
	if resp := api.Post("/api/v1/clusters/1/members", map[string]any{"mac": mac, "machineType": "controlplane"}); resp.Code != 200 {
		t.Fatalf("bind to A = %d: %s", resp.Code, resp.Body.String())
	}
	// Import naming that same host as the CP must be refused, must not steal it,
	// and must not create the cluster.
	cp := string(genControlPlaneBytes(t, "controlplane"))
	resp := api.Post("/api/v1/clusters/import", map[string]any{
		"name": "steal", "controlplane": cp, "controlplaneMac": mac,
	})
	if resp.Code != 422 {
		t.Fatalf("import stealing a member = %d, want 422", resp.Code)
	}
	h, _ := hardware.GetMacAddress(mac)
	if h.ClusterID == nil || *h.ClusterID != 1 {
		t.Fatalf("host was stolen from cluster A: %+v", h)
	}
	if list, _ := deps.Store.ListClusters(); len(list) != 1 {
		t.Fatalf("failed import created a cluster: %+v", list)
	}
}

func TestImportRejectsNonTalosHost(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	const mac = "aa:bb:cc:dd:ee:f2"
	hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar"})
	cp := string(genControlPlaneBytes(t, "controlplane"))
	resp := api.Post("/api/v1/clusters/import", map[string]any{
		"name": "nontalos", "controlplane": cp, "controlplaneMac": mac,
	})
	if resp.Code != 422 {
		t.Fatalf("import onto a non-talos host = %d, want 422", resp.Code)
	}
	if list, _ := deps.Store.ListClusters(); len(list) != 0 {
		t.Fatalf("failed import created a cluster: %+v", list)
	}
}

func TestImportRejectsWorkerOnlyAtAPI(t *testing.T) {
	deps := clustersTestSetup(t)
	api := newTestAPI(t, deps)
	w := string(genControlPlaneBytes(t, "worker"))
	resp := api.Post("/api/v1/clusters/import", map[string]any{
		"name": "wonly", "controlplane": w, "controlplaneMac": "aa:bb:cc:dd:ee:c4",
	})
	if resp.Code != 422 {
		t.Fatalf("worker-only import = %d, want 422", resp.Code)
	}
}
