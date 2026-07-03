package http

import (
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
)

func resolveStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// mkConfig creates a config + revision-1 + sets active, returning the id.
func mkConfig(t *testing.T, s *db.Store, name, kind, source string) int64 {
	t.Helper()
	id, err := s.CreateConfig(name, kind)
	if err != nil {
		t.Fatal(err)
	}
	rid, _, err := s.AddConfigRevision(id, base64.StdEncoding.EncodeToString([]byte(source)), "sha")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetActiveRevision(id, rid); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestResolveConfigHostConfigIDWins(t *testing.T) {
	s := resolveStore(t)
	cid := mkConfig(t, s, "host-cfg", "butane", "variant: fcos")
	src, kind, ok := resolveConfig(s, &hardware.Host{MAC: "aa:bb:cc:dd:ee:20", OS: "flatcar", ConfigID: &cid})
	if !ok || kind != "butane" || string(src) != "variant: fcos" {
		t.Fatalf("rung 1 = (%q,%q,%v), want (variant: fcos, butane, true)", src, kind, ok)
	}
}

func TestResolveConfigRoleDefaultByName(t *testing.T) {
	s := resolveStore(t)
	const mac = "aa:bb:cc:dd:ee:21"
	if err := s.UpsertHost(db.Host{MAC: mac}); err != nil {
		t.Fatal(err)
	}
	cid := mkConfig(t, s, "role-cfg", "butane", "role-source")
	// Two roles; only the alphabetically-first with a non-nil default should win.
	rWorker, _ := s.CreateRole("worker", &cid)
	rApex, _ := s.CreateRole("apex", nil) // no default; earlier by name but skipped
	if err := s.SetHostRoles(mac, []int64{rWorker, rApex}); err != nil {
		t.Fatal(err)
	}
	src, kind, ok := resolveConfig(s, &hardware.Host{MAC: mac, OS: "flatcar"})
	if !ok || kind != "butane" || string(src) != "role-source" {
		t.Fatalf("rung 2 = (%q,%q,%v), want role-source/butane/true", src, kind, ok)
	}
}

func TestResolveConfigUnboundFallsThrough(t *testing.T) {
	s := resolveStore(t)
	if _, _, ok := resolveConfig(s, &hardware.Host{MAC: "aa:bb:cc:dd:ee:22", OS: "flatcar"}); ok {
		t.Fatal("unbound host must return ok=false (serving falls to the file path)")
	}
}

func TestResolveConfigFamilyMismatchFallsThrough(t *testing.T) {
	s := resolveStore(t)
	// A talos host bound to a butane config: guard fails → fall through.
	cid := mkConfig(t, s, "wrong", "butane", "x")
	if _, _, ok := resolveConfig(s, &hardware.Host{MAC: "aa:bb:cc:dd:ee:23", OS: "talos", ConfigID: &cid}); ok {
		t.Fatal("family-mismatched config must never serve (ok=false)")
	}
}

// TestResolveConfigExplicitMismatchDoesNotSubstituteRoleDefault pins F4 / design
// §5: a rung-1 EXPLICIT per-host binding whose kind mismatches the host family
// short-circuits to ok=false (→ file fallback) even when a VALID rung-2 role
// default exists — an explicit-but-wrong bind is surfaced, never silently
// replaced by a role config.
func TestResolveConfigExplicitMismatchDoesNotSubstituteRoleDefault(t *testing.T) {
	s := resolveStore(t)
	const mac = "aa:bb:cc:dd:ee:25"
	if err := s.UpsertHost(db.Host{MAC: mac}); err != nil {
		t.Fatal(err)
	}
	// Explicit per-host binding to a WRONG-family (butane) config for a talos host.
	wrong := mkConfig(t, s, "wrong", "butane", "x")
	// A VALID (machineconfig) role default is also present — it must NOT be used.
	right := mkConfig(t, s, "right", "machineconfig", "machine: {}")
	rid, _ := s.CreateRole("controlplane", &right)
	if err := s.SetHostRoles(mac, []int64{rid}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := resolveConfig(s, &hardware.Host{MAC: mac, OS: "talos", ConfigID: &wrong}); ok {
		t.Fatal("explicit rung-1 mismatch must return ok=false (file fallback), not substitute the valid role default")
	}
}

func TestResolveConfigUnknownOSFallsThrough(t *testing.T) {
	s := resolveStore(t)
	cid := mkConfig(t, s, "c", "butane", "x")
	if _, _, ok := resolveConfig(s, &hardware.Host{MAC: "aa:bb:cc:dd:ee:24", OS: "", ConfigID: &cid}); ok {
		t.Fatal("empty/unknown OS (family lookup miss) must fall through")
	}
}
