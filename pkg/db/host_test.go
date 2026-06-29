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
		IgnitionFile: "config/custom.yaml", OS: "talos", DoInstall: true, Schematic: "abc"}
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
