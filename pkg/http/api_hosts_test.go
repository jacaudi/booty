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

// TestDeleteHostRemovesTheHost REPLACES the pre-existing TestDeleteHostIs403
// (api_hosts_test.go:47). delete-host stops being a stub in this task:
// POST /unregister was retired on the grounds that this endpoint supersedes
// it, and that has to be true or host deletion disappears entirely.
func TestDeleteHostRemovesTheHost(t *testing.T) {
	api := newTestAPI(t, hostsTestSetup(t))
	const mac = "aa:bb:cc:dd:ee:04"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar"}); err != nil {
		t.Fatal(err)
	}

	if resp := api.Delete("/api/v1/hosts/" + mac); resp.Code != 204 {
		t.Fatalf("DELETE existing host = %d, want 204 (body %s)", resp.Code, resp.Body.String())
	}
	if _, err := hardware.GetMacAddress(mac); err == nil {
		t.Fatal("the host still exists after DELETE")
	}
	if resp := api.Delete("/api/v1/hosts/aa:bb:cc:dd:ee:99"); resp.Code != 404 {
		t.Fatalf("DELETE absent host = %d, want 404", resp.Code)
	}
}

// TestDeleteHostAbsentMACDoesNotPolluteUnknownHosts is the regression guard
// for the phantom-machine bug: DELETE on an absent MAC used to call
// hardware.GetMacAddress purely as an existence check, whose miss path calls
// trackUnknown -- so one mistyped MAC would permanently appear in the
// operator's Hosts view (unknown array) until a later exact-match lookup or
// a restart. A 404 must never have that side effect.
func TestDeleteHostAbsentMACDoesNotPolluteUnknownHosts(t *testing.T) {
	api := newTestAPI(t, hostsTestSetup(t))
	if resp := api.Delete("/api/v1/hosts/de:ad:be:ef:00:01"); resp.Code != 404 {
		t.Fatalf("DELETE absent host = %d, want 404", resp.Code)
	}
	if got := hardware.ListUnknownHosts(); len(got) != 0 {
		t.Fatalf("a 404 from delete-host must not add the MAC to ListUnknownHosts, got %v", got)
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

// TestBindCoreOSHostSucceeds pins the fix for the CoreOS binding bug: a host
// whose OS is "coreos" (booty's short/boot vocabulary) must bind a valid
// butane config (its ignition-family kind, once bridged to the ostype
// taxonomy's "fedora-coreos") with 200, not the pre-fix 422 "config kind does
// not match host OS family" caused by osFamily's raw ostype.Lookup("coreos")
// miss.
func TestBindCoreOSHostSucceeds(t *testing.T) {
	deps := hostsTestDeps(t)
	api := newTestAPI(t, deps)
	if err := hardware.WriteMacAddress("aa:bb:cc:dd:ee:42", hardware.Host{MAC: "aa:bb:cc:dd:ee:42", OS: "coreos"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cid, err := deps.Store.CreateConfig("coreos-cfg", "butane")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	resp := api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:42/bind", map[string]any{"configId": cid})
	if resp.Code != 200 {
		t.Fatalf("coreos bind = %d, want 200: %s", resp.Code, resp.Body.String())
	}
	h, err := deps.Store.GetHost("aa:bb:cc:dd:ee:42")
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if h.ConfigID == nil || *h.ConfigID != cid {
		t.Fatalf("config not bound: %v", h.ConfigID)
	}
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

// TestApproveInvalidBindingLeavesHostUnapproved pins the validate-before-approve
// fix in the approve handler: a family-mismatched config (or any other
// validation failure) must be caught BEFORE hardware.Approve/SetAssignment run,
// so the host stays pending/unapproved and unbound. Before the fix, approve
// wrote Approve+SetAssignment first and only then validated the binding via
// bindHostConfigRoles, so this exact request left the host approved (and
// booting the server-default config) despite the 422.
func TestApproveInvalidBindingLeavesHostUnapproved(t *testing.T) {
	deps := hostsTestDeps(t) // host OS = flatcar (ignition family → butane)
	api := newTestAPI(t, deps)
	cid, err := deps.Store.CreateConfig("talos-cfg", "machineconfig") // wrong kind for flatcar
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	resp := api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:40/approve", map[string]any{"configId": cid})
	if resp.Code != 422 {
		t.Fatalf("approve with mismatched config = %d, want 422: %s", resp.Code, resp.Body.String())
	}
	h, err := deps.Store.GetHost("aa:bb:cc:dd:ee:40")
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if h.Approved {
		t.Fatalf("validation failure must leave host unapproved, but Approved=true")
	}
	if h.ConfigID != nil {
		t.Fatalf("validation failure must bind nothing, but config was persisted: %v", *h.ConfigID)
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

func TestCreateHostCreatesThenUpdates(t *testing.T) {
	api := newTestAPI(t, hostsTestSetup(t))

	resp := api.Post("/api/v1/hosts", map[string]any{
		"mac": "aa:bb:cc:dd:ee:01", "hostname": "node1", "os": "talos",
	})
	if resp.Code != 201 {
		t.Fatalf("first POST /api/v1/hosts = %d, want 201 (body %s)", resp.Code, resp.Body.String())
	}

	again := api.Post("/api/v1/hosts", map[string]any{
		"mac": "aa:bb:cc:dd:ee:01", "hostname": "node1-renamed", "os": "talos",
	})
	if again.Code != 200 {
		t.Fatalf("second POST for the same MAC = %d, want 200 (upsert, not 201)", again.Code)
	}

	list := api.Get("/api/v1/hosts")
	if !strings.Contains(list.Body.String(), "node1-renamed") {
		t.Fatalf("the update did not persist: %s", list.Body.String())
	}
}

// TestCreateHostUpdatePreservesUnsuppliedColumns is the data-loss guard for
// P3, and it is the reason the handler must read-modify-write instead of
// calling WriteMacAddress with a freshly-built Host.
//
// db.UpsertHost (pkg/db/host.go:75-94) overwrites hostname, ip, booted,
// ignition_file, os, do_install and schematic from the incoming row on
// conflict. A handler that leaves them zero therefore BLANKS the host's
// recorded IP, its last-boot marker, its one-shot-install flag, and any
// operator-set ignition_file on every update.
//
// TestCreateHostCreatesThenUpdates cannot catch this: it supplies every field
// it asserts on. This one seeds the columns the DTO cannot carry and proves
// they survive.
func TestCreateHostUpdatePreservesUnsuppliedColumns(t *testing.T) {
	api := newTestAPI(t, hostsTestSetup(t))
	const mac = "aa:bb:cc:dd:ee:03"

	// Seed a host the way the boot path would, with fields the create DTO has
	// no way to send.
	if err := hardware.WriteMacAddress(mac, hardware.Host{
		MAC: mac, Hostname: "seeded", IP: "10.0.0.9", Booted: "2026-08-01T00:00:00Z",
		OS: "flatcar", DoInstall: true, IgnitionFile: "config/custom.yaml",
	}); err != nil {
		t.Fatal(err)
	}

	// Change ONLY the OS, exactly the workflow P3 exists to support.
	resp := api.Post("/api/v1/hosts", map[string]any{"mac": mac, "os": "talos"})
	if resp.Code != 200 {
		t.Fatalf("update = %d, want 200 (body %s)", resp.Code, resp.Body.String())
	}

	after, err := hardware.GetMacAddress(mac)
	if err != nil {
		t.Fatal(err)
	}
	if after.OS != "talos" {
		t.Errorf("os = %q, want talos (the update must apply)", after.OS)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"hostname", after.Hostname, "seeded"},
		{"ip", after.IP, "10.0.0.9"},
		{"booted", after.Booted, "2026-08-01T00:00:00Z"},
		{"ignitionFile", after.IgnitionFile, "config/custom.yaml"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q — an omitted field must not be blanked", tc.name, tc.got, tc.want)
		}
	}
	if !after.DoInstall {
		t.Error("doInstall = false, want true — an omitted field must not be blanked")
	}
}

func TestCreateHostRequiresAMAC(t *testing.T) {
	api := newTestAPI(t, hostsTestSetup(t))

	resp := api.Post("/api/v1/hosts", map[string]any{"hostname": "no-mac"})
	if resp.Code != 422 {
		t.Fatalf("POST without mac = %d, want 422", resp.Code)
	}
	// huma validates the schema before the handler, so assert on the BODY:
	// otherwise a control case that also 422s would make this test prove nothing.
	if !strings.Contains(strings.ToLower(resp.Body.String()), "mac") {
		t.Fatalf("the 422 must name the missing field: %s", resp.Body.String())
	}
}

// TestCreateHostRejectsAnInvalidMAC covers the boundary huma cannot: the
// schema accepts any string, and NormalizeMAC is what rejects a malformed one.
// It must be a 422 like every sibling handler (api_hosts.go:135,197,236), not
// the 500 a naive "anything that is not ErrNotFound" branch would produce.
func TestCreateHostRejectsAnInvalidMAC(t *testing.T) {
	api := newTestAPI(t, hostsTestSetup(t))

	resp := api.Post("/api/v1/hosts", map[string]any{"mac": "not-a-mac", "os": "flatcar"})
	if resp.Code != 422 {
		t.Fatalf("POST with a malformed mac = %d, want 422 (body %s)", resp.Code, resp.Body.String())
	}
}

// TestCreateHostRejectsAnUnknownOS closes the input-validation gap on the
// field create-target already validates two files over (api_targets.go:106-
// 108, via ostype.Lookup): before this fix, {"os":"talso"} (a typo of
// "talos") returned 201 and was written straight into assigned_os, so the
// operator only discovered the typo at netboot time. os is OPTIONAL, so an
// empty/omitted value must still be accepted -- the control case below
// proves that and also that a genuinely valid OS still creates the host.
func TestCreateHostRejectsAnUnknownOS(t *testing.T) {
	api := newTestAPI(t, hostsTestSetup(t))

	resp := api.Post("/api/v1/hosts", map[string]any{"mac": "aa:bb:cc:dd:ee:10", "os": "talso"})
	if resp.Code != 422 {
		t.Fatalf("POST with an unknown os = %d, want 422 (body %s)", resp.Code, resp.Body.String())
	}
	if !strings.Contains(strings.ToLower(resp.Body.String()), "os") {
		t.Fatalf("the 422 must name the os field: %s", resp.Body.String())
	}

	// Control: a genuinely valid OS must still create the host.
	control := api.Post("/api/v1/hosts", map[string]any{"mac": "aa:bb:cc:dd:ee:11", "os": "talos"})
	if control.Code != 201 {
		t.Fatalf("POST with a valid os = %d, want 201 (body %s)", control.Code, control.Body.String())
	}
}

// TestCreateHostAllowsAnOmittedOS proves os stays optional: the validation
// added for the unknown-OS case above must not reject an absent one.
func TestCreateHostAllowsAnOmittedOS(t *testing.T) {
	api := newTestAPI(t, hostsTestSetup(t))

	resp := api.Post("/api/v1/hosts", map[string]any{"mac": "aa:bb:cc:dd:ee:12"})
	if resp.Code != 201 {
		t.Fatalf("POST with no os = %d, want 201 (body %s)", resp.Code, resp.Body.String())
	}
}

// TestCreateHostOSValidationMatchesTheBootPathVocabulary is the regression
// guard for a fix to the fix: create-host originally validated os with the
// SAME bare ostype.Lookup create-target uses, but that registry has no
// "coreos" entry -- only "fedora-coreos" (pkg/ostype/ignition.go). The boot
// path has always accepted "coreos": resolve.go's osFamily canonicalizes via
// cache.CacheNameToCanonical before looking up, which is the single source of
// the "coreos" <-> "fedora-coreos" bridge. Validating with the bare lookup
// made create-host STRICTER than its own consumer, so {"os":"coreos"} -- a
// value booty uses in its own vocabulary (GET /api/v1/info's "coreos" key,
// --coreosArchitecture, the CLI's own description) and one the retired
// POST /register accepted unvalidated -- regressed from working to a 422.
// The validator must match the consumer (osFamily), not a different one
// (ostype.Lookup) that happens to be nearby.
func TestCreateHostOSValidationMatchesTheBootPathVocabulary(t *testing.T) {
	cases := []struct {
		name string
		mac  string
		os   string
		want int
	}{
		{name: "coreos (boot-path short name) creates", mac: "aa:bb:cc:dd:ee:20", os: "coreos", want: 201},
		{name: "fedora-coreos (canonical spelling) creates", mac: "aa:bb:cc:dd:ee:21", os: "fedora-coreos", want: 201},
		{name: "flatcar (control) creates", mac: "aa:bb:cc:dd:ee:22", os: "flatcar", want: 201},
		{name: "talso (typo) is rejected", mac: "aa:bb:cc:dd:ee:23", os: "talso", want: 422},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newTestAPI(t, hostsTestSetup(t))
			resp := api.Post("/api/v1/hosts", map[string]any{"mac": tc.mac, "os": tc.os})
			if resp.Code != tc.want {
				t.Fatalf("POST os=%q = %d, want %d (body %s)", tc.os, resp.Code, tc.want, resp.Body.String())
			}
			if tc.want == 422 && !strings.Contains(strings.ToLower(resp.Body.String()), "os") {
				t.Fatalf("the 422 must name the os field: %s", resp.Body.String())
			}
		})
	}

	// os stays optional even with the validator swapped.
	t.Run("no os still creates", func(t *testing.T) {
		api := newTestAPI(t, hostsTestSetup(t))
		resp := api.Post("/api/v1/hosts", map[string]any{"mac": "aa:bb:cc:dd:ee:24"})
		if resp.Code != 201 {
			t.Fatalf("POST with no os = %d, want 201 (body %s)", resp.Code, resp.Body.String())
		}
	})
}

// TestCreateHostRejectsAnIgnitionFileField is the P2 guarantee: the create DTO
// deliberately has no ignitionFile, so a client cannot reintroduce the
// path-traversal writer that POST /register was.
//
// huma sets additionalProperties:false by default (schema.go:956,968;
// validate.go:702), so an unknown property is a 422 — NOT an ignored field.
// Verified against v2.38.0 this session:
//
//	{"mac":...,"ignitionFile":"../../etc/passwd"}
//	-> 422 {"errors":[{"message":"unexpected property","location":"body.ignitionFile"}]}
//
// Rejecting outright is strictly better than ignoring, so this asserts the 422.
func TestCreateHostRejectsAnIgnitionFileField(t *testing.T) {
	deps := hostsTestSetup(t)
	api := newTestAPI(t, deps)

	resp := api.Post("/api/v1/hosts", map[string]any{
		"mac": "aa:bb:cc:dd:ee:02", "os": "flatcar", "ignitionFile": "../../etc/passwd",
	})
	if resp.Code != 422 {
		t.Fatalf("unknown body property = %d, want 422 (body %s)", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "ignitionFile") {
		t.Fatalf("the 422 must name the rejected property: %s", resp.Body.String())
	}
	// And nothing was written.
	if _, err := hardware.GetMacAddress("aa:bb:cc:dd:ee:02"); err == nil {
		t.Fatal("a rejected create must not persist a host")
	}
}

// TestListHostsIncludesUnknownHosts guards the guarantee that GET /api/v1/hosts
// genuinely supersedes the retired GET /booty.json. Unknown hosts live only in
// an in-memory map (hardware/mac.go:50, trackUnknown at :509) and nothing ever
// writes them to the DB, so ListHosts alone cannot surface them.
func TestListHostsIncludesUnknownHosts(t *testing.T) {
	api := newTestAPI(t, hostsTestSetup(t))
	const known = "aa:bb:cc:dd:ee:05"
	if err := hardware.WriteMacAddress(known, hardware.Host{MAC: known, OS: "flatcar"}); err != nil {
		t.Fatal(err)
	}
	// A lookup miss is what records an unknown host.
	if _, err := hardware.GetMacAddress("aa:bb:cc:dd:ee:06"); err == nil {
		t.Fatal("precondition: that MAC must not be registered")
	}

	resp := api.Get("/api/v1/hosts")
	if resp.Code != 200 {
		t.Fatalf("list = %d, want 200", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, known) {
		t.Errorf("registered host missing from the listing: %s", body)
	}
	if !strings.Contains(body, "aa:bb:cc:dd:ee:06") {
		t.Errorf("unknown host missing from the listing: %s", body)
	}
	if !strings.Contains(body, `"unknown"`) {
		t.Errorf("the listing must carry an \"unknown\" field: %s", body)
	}
}

// TestListHostsUnknownIsEmptyArrayNotNull guards the nil-slice footgun
// api_hosts.go's list-hosts handler works around: hardware.ListUnknownHosts
// calls slices.Sorted over a possibly-empty map, which returns a nil slice,
// and a nil slice marshals to JSON null -- which the UI's flatMap would then
// surface AS AN ELEMENT of the unknown list, not as an empty list. With no
// unknown hosts recorded, the listing must carry "unknown":[], never
// "unknown":null.
func TestListHostsUnknownIsEmptyArrayNotNull(t *testing.T) {
	api := newTestAPI(t, hostsTestSetup(t))

	resp := api.Get("/api/v1/hosts")
	if resp.Code != 200 {
		t.Fatalf("list = %d, want 200", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"unknown":[]`) {
		t.Errorf(`listing with no unknown hosts must carry "unknown":[], got: %s`, body)
	}
	if strings.Contains(body, `"unknown":null`) {
		t.Errorf(`listing must never carry "unknown":null: %s`, body)
	}
}
