package db

import (
	"database/sql"
	"errors"
	"testing"
)

func TestHost_UpsertGetDelete(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.GetHost("aa:bb:cc:dd:ee:ff"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetHost miss: err = %v, want sql.ErrNoRows", err)
	}

	in := Host{MAC: "aa:bb:cc:dd:ee:ff", Hostname: "node-01", IP: "10.0.0.5",
		IgnitionFile: "config/custom.yaml", OS: "talos", DoInstall: true, Schematic: "abc",
		// Schema defaults returned by GetHost after scanHost was widened (P1c).
		BootMode: "menu", AssignedParams: "{}",
	}
	if err := s.UpsertHost(in); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	got, err := s.GetHost("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if *got != in {
		t.Errorf("GetHost = %+v, want %+v", *got, in)
	}

	// Update path.
	in.Hostname = "node-renamed"
	if err := s.UpsertHost(in); err != nil {
		t.Fatalf("UpsertHost update: %v", err)
	}
	if got, _ := s.GetHost(in.MAC); got.Hostname != "node-renamed" {
		t.Errorf("Hostname = %q after update, want node-renamed", got.Hostname)
	}

	if err := s.DeleteHost(in.MAC); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	if _, err := s.GetHost(in.MAC); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("after delete, GetHost err = %v, want sql.ErrNoRows", err)
	}
}

func TestHost_DeleteIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	if err := s.DeleteHost("11:22:33:44:55:66"); err != nil {
		t.Errorf("DeleteHost on absent: err = %v, want nil", err)
	}
}

func TestHost_UpsertPreservesFutureColumns(t *testing.T) {
	s := newTestStore(t)
	// Simulate a future slice having set approved=1, boot_mode='assigned'.
	if _, err := s.db.Exec(
		`INSERT INTO hosts (mac, approved, boot_mode) VALUES (?, 1, 'assigned')`,
		"aa:bb:cc:dd:ee:ff"); err != nil {
		t.Fatalf("seed future cols: %v", err)
	}
	// A legacy UpsertHost must NOT reset them.
	if err := s.UpsertHost(Host{MAC: "aa:bb:cc:dd:ee:ff", Hostname: "n1"}); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
	var approved int
	var bootMode string
	if err := s.db.QueryRow(
		`SELECT approved, boot_mode FROM hosts WHERE mac = ?`, "aa:bb:cc:dd:ee:ff",
	).Scan(&approved, &bootMode); err != nil {
		t.Fatalf("read: %v", err)
	}
	if approved != 1 || bootMode != "assigned" {
		t.Errorf("future columns clobbered: approved=%d boot_mode=%q, want 1/assigned", approved, bootMode)
	}
}

func TestListHosts(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertHost(Host{MAC: "aa:bb:cc:dd:ee:01", Hostname: "a"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.UpsertHost(Host{MAC: "aa:bb:cc:dd:ee:02", Hostname: "b"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	all, err := s.ListHosts()
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListHosts returned %d, want 2", len(all))
	}
}

func TestApproveAndAssign(t *testing.T) {
	s := newTestStore(t) // existing helper in this package's tests
	if err := s.UpsertHost(Host{MAC: "aa:bb:cc:dd:ee:ff", OS: "talos"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Fresh host defaults: unapproved, menu.
	h, err := s.GetHost("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if h.Approved || h.BootMode != "menu" {
		t.Fatalf("fresh host = approved %v mode %q, want false/menu", h.Approved, h.BootMode)
	}

	if err := s.ApproveHost("aa:bb:cc:dd:ee:ff"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := s.SetAssignment("aa:bb:cc:dd:ee:ff", "talos", "amd64", `{"schematic":"x"}`); err != nil {
		t.Fatalf("assign: %v", err)
	}
	h, _ = s.GetHost("aa:bb:cc:dd:ee:ff")
	if !h.Approved || h.BootMode != "assigned" || h.AssignedOS != "talos" || h.AssignedParams != `{"schematic":"x"}` {
		t.Fatalf("after approve+assign = %+v", *h)
	}

	// Re-registration via UpsertHost must NOT clobber approval/assignment.
	if err := s.UpsertHost(Host{MAC: "aa:bb:cc:dd:ee:ff", OS: "talos", Hostname: "node1"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	h, _ = s.GetHost("aa:bb:cc:dd:ee:ff")
	if !h.Approved || h.BootMode != "assigned" || h.Hostname != "node1" {
		t.Fatalf("re-registration clobbered state: %+v", *h)
	}

	if err := s.RevokeHost("aa:bb:cc:dd:ee:ff"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	h, _ = s.GetHost("aa:bb:cc:dd:ee:ff")
	if h.Approved {
		t.Fatalf("revoke did not clear approved")
	}
}

func TestSetBootMode(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertHost(Host{MAC: "aa:bb:cc:00:00:01", OS: "talos"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.SetBootMode("aa:bb:cc:00:00:01", "menu"); err != nil {
		t.Fatalf("SetBootMode: %v", err)
	}
	h, err := s.GetHost("aa:bb:cc:00:00:01")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if h.BootMode != "menu" {
		t.Fatalf("BootMode = %q, want menu", h.BootMode)
	}
}

func TestPreserveExistingHostBoot(t *testing.T) {
	s := newTestStore(t)
	// Pre-existing registered host (has an OS) + a never-configured host.
	_ = s.UpsertHost(Host{MAC: "11:11:11:11:11:11", OS: "flatcar"})
	_ = s.UpsertHost(Host{MAC: "22:22:22:22:22:22"}) // os == ""

	n, err := s.PreserveExistingHostBoot()
	if err != nil {
		t.Fatalf("preserve: %v", err)
	}
	if n != 1 {
		t.Fatalf("preserved %d hosts, want 1", n)
	}
	h, _ := s.GetHost("11:11:11:11:11:11")
	if !h.Approved || h.BootMode != "assigned" || h.AssignedOS != "flatcar" {
		t.Fatalf("configured host not preserved: %+v", *h)
	}
	h2, _ := s.GetHost("22:22:22:22:22:22")
	if h2.Approved {
		t.Fatalf("never-configured host should stay unapproved")
	}

	// Idempotent: second run is gated by the meta flag and touches nothing.
	n, err = s.PreserveExistingHostBoot()
	if err != nil || n != 0 {
		t.Fatalf("second preserve = (%d,%v), want (0,nil)", n, err)
	}
}
