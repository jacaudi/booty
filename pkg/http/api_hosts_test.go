package http

import (
	"path/filepath"
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
