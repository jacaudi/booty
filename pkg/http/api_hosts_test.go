package http

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
)

func hostsTestSetup(t *testing.T) APIDeps {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
		// Reset the hardware package's injected store so subsequent tests
		// that call hardware.Load() without SetStore() can open their own.
		hardware.SetStore(nil)
	})
	hardware.SetStore(store)
	if err := hardware.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	return APIDeps{Store: store, Trigger: func() {}}
}

func TestApproveHostSetsAssigned(t *testing.T) {
	deps := hostsTestSetup(t)
	api := newTestAPI(t, deps)
	if err := hardware.WriteMacAddress("aa:bb:cc:00:00:01", hardware.Host{MAC: "aa:bb:cc:00:00:01", OS: "flatcar"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp := api.Post("/api/v1/hosts/aa:bb:cc:00:00:01/approve", map[string]any{})
	if resp.Code != 200 && resp.Code != 204 {
		t.Fatalf("approve = %d: %s", resp.Code, resp.Body.String())
	}
	h, _ := hardware.GetMacAddress("aa:bb:cc:00:00:01")
	if !h.Approved || h.BootMode != "assigned" || h.AssignedOS != "flatcar" {
		t.Fatalf("after approve: %+v", *h)
	}
}

func TestDeleteHostIs403(t *testing.T) {
	deps := hostsTestSetup(t)
	api := newTestAPI(t, deps)
	resp := api.Delete("/api/v1/hosts/aa:bb:cc:00:00:09")
	if resp.Code != 403 {
		t.Fatalf("DELETE host = %d, want 403", resp.Code)
	}
}

func TestMenuHostSetsMenuMode(t *testing.T) {
	deps := hostsTestSetup(t)
	api := newTestAPI(t, deps)
	if err := hardware.WriteMacAddress("aa:bb:cc:00:00:03", hardware.Host{MAC: "aa:bb:cc:00:00:03", OS: "talos"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp := api.Post("/api/v1/hosts/aa:bb:cc:00:00:03/menu", map[string]any{})
	if resp.Code != 200 {
		t.Fatalf("menu = %d: %s", resp.Code, resp.Body.String())
	}
	h, _ := hardware.GetMacAddress("aa:bb:cc:00:00:03")
	if !h.Approved || h.BootMode != "menu" {
		t.Fatalf("after menu: approved=%v bootMode=%q, want approved + menu", h.Approved, h.BootMode)
	}
}

func TestMenuHostUnknownMAC404(t *testing.T) {
	deps := hostsTestSetup(t)
	api := newTestAPI(t, deps)
	resp := api.Post("/api/v1/hosts/aa:bb:cc:00:00:ff/menu", map[string]any{})
	if resp.Code != 404 {
		t.Fatalf("unknown MAC menu = %d, want 404", resp.Code)
	}
	// Verify this is the handler's 404 (huma problem+json with our message), not
	// a mux catch-all 404 — the latter would not contain "host not found".
	if !strings.Contains(resp.Body.String(), "host not found") {
		t.Fatalf("want 'host not found' in 404 body, got: %s", resp.Body.String())
	}
}

// hostsTestDeps mirrors hostsTestSetup (reusing its store/hardware wiring) and
// additionally seeds a single approved-candidate flatcar host used by the P4
// bind/approve-with-body tests below.
func hostsTestDeps(t *testing.T) APIDeps {
	t.Helper()
	deps := hostsTestSetup(t)
	if err := hardware.WriteMacAddress("aa:bb:cc:dd:ee:40", hardware.Host{MAC: "aa:bb:cc:dd:ee:40", OS: "flatcar"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return deps
}

func TestApproveEmptyBodyBackwardCompatible(t *testing.T) {
	deps := hostsTestDeps(t)
	api := newTestAPI(t, deps)
	// A genuinely OMITTED body (no second arg to api.Post — a zero-byte request,
	// exactly what the frontend sends) must behave exactly like today's approve
	// (approve + assign). This is the case a huma non-pointer Body field would
	// reject with 400 "request body is required" before the handler even runs;
	// passing map[string]any{} here would marshal to "{}" and mask that bug.
	resp := api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:40/approve")
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), `"approved":true`) {
		t.Fatalf("approve empty = %d: %s", resp.Code, resp.Body.String())
	}
}

func TestApproveWithConfigAndRolesAtomic(t *testing.T) {
	deps := hostsTestDeps(t)
	api := newTestAPI(t, deps)
	cid, err := deps.Store.CreateConfig("cfg", "butane")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	rid, err := deps.Store.CreateRole("cp", nil)
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	resp := api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:40/approve", map[string]any{
		"configId": cid, "roleIds": []int64{rid},
	})
	if resp.Code != 200 {
		t.Fatalf("approve+attach = %d: %s", resp.Code, resp.Body.String())
	}
	h, err := deps.Store.GetHost("aa:bb:cc:dd:ee:40")
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if h.ConfigID == nil || *h.ConfigID != cid {
		t.Fatalf("config not bound: %v", h.ConfigID)
	}
	roles, err := deps.Store.ListHostRoles("aa:bb:cc:dd:ee:40")
	if err != nil {
		t.Fatalf("list host roles: %v", err)
	}
	if len(roles) != 1 || roles[0].ID != rid {
		t.Fatalf("roles not bound: %+v", roles)
	}
}

func TestBindFamilyMismatchIs422(t *testing.T) {
	deps := hostsTestDeps(t) // host OS = flatcar (ignition family → butane)
	api := newTestAPI(t, deps)
	cid, err := deps.Store.CreateConfig("talos-cfg", "machineconfig") // wrong kind for flatcar
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	resp := api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:40/bind", map[string]any{"configId": cid})
	if resp.Code != 422 {
		t.Fatalf("family mismatch bind = %d, want 422: %s", resp.Code, resp.Body.String())
	}
}

func TestBindRebindsApprovedHost(t *testing.T) {
	deps := hostsTestDeps(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:40/approve", map[string]any{})
	cid, err := deps.Store.CreateConfig("cfg", "butane")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	resp := api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:40/bind", map[string]any{"configId": cid})
	if resp.Code != 200 {
		t.Fatalf("bind = %d: %s", resp.Code, resp.Body.String())
	}
	h, err := deps.Store.GetHost("aa:bb:cc:dd:ee:40")
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if h.ConfigID == nil || *h.ConfigID != cid {
		t.Fatalf("rebind failed: %v", h.ConfigID)
	}
}

// TestBindEmptyBodyIsNoOp proves the pointer-Body fix works both directions:
// a genuinely omitted body on /bind is accepted (no 400 "request body is
// required") and leaves the host's config/roles untouched, since
// bindHostConfigRoles is skipped entirely when in.Body == nil.
func TestBindEmptyBodyIsNoOp(t *testing.T) {
	deps := hostsTestDeps(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:40/approve")
	resp := api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:40/bind")
	if resp.Code != 200 {
		t.Fatalf("bind empty body = %d: %s", resp.Code, resp.Body.String())
	}
	h, err := deps.Store.GetHost("aa:bb:cc:dd:ee:40")
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if h.ConfigID != nil {
		t.Fatalf("empty-body bind must not bind a config, got: %v", h.ConfigID)
	}
}

// TestBindUnknownOSFamilyIs422 exercises bindHostConfigRoles' osFamily
// lookup-miss branch: a host whose OS is unrecognized (empty string) has no
// family, so any config bind must 422 rather than panic or silently succeed.
func TestBindUnknownOSFamilyIs422(t *testing.T) {
	deps := hostsTestDeps(t)
	api := newTestAPI(t, deps)
	if err := hardware.WriteMacAddress("aa:bb:cc:dd:ee:41", hardware.Host{MAC: "aa:bb:cc:dd:ee:41", OS: ""}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cid, err := deps.Store.CreateConfig("cfg", "butane")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	resp := api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:41/bind", map[string]any{"configId": cid})
	if resp.Code != 422 {
		t.Fatalf("unknown OS family bind = %d, want 422: %s", resp.Code, resp.Body.String())
	}
}

// TestBindValidConfigInvalidRoleBindsNothing pins the validate-all-then-write
// fix in bindHostConfigRoles: a request with a VALID config but a
// nonexistent role must fail the whole bind — including the config half —
// not persist the config and then 422 on the role. Before the fix,
// bindHostConfigRoles wrote the config binding before validating roles, so
// this exact request left the host partially bound despite the 422.
func TestBindValidConfigInvalidRoleBindsNothing(t *testing.T) {
	deps := hostsTestDeps(t) // host OS = flatcar (ignition family → butane)
	api := newTestAPI(t, deps)
	cid, err := deps.Store.CreateConfig("cfg", "butane")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	resp := api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:40/bind", map[string]any{
		"configId": cid, "roleIds": []int64{99999},
	})
	if resp.Code != 422 {
		t.Fatalf("valid config + invalid role bind = %d, want 422: %s", resp.Code, resp.Body.String())
	}
	h, err := deps.Store.GetHost("aa:bb:cc:dd:ee:40")
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if h.ConfigID != nil {
		t.Fatalf("validation failure must bind nothing, but config was persisted: %v", *h.ConfigID)
	}
	roles, err := deps.Store.ListHostRoles("aa:bb:cc:dd:ee:40")
	if err != nil {
		t.Fatalf("list host roles: %v", err)
	}
	if len(roles) != 0 {
		t.Fatalf("validation failure must bind nothing, but roles were persisted: %+v", roles)
	}
}
