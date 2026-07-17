package db

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "booty.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAndGetTarget(t *testing.T) {
	s := newTestStore(t)

	id, err := s.CreateTarget(Target{
		OS: "talos", Arch: "amd64", Params: `{"schematic":"abc"}`,
		Mode: "discovery", RetainN: 3, Source: "catalog", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateTarget returned id 0")
	}

	got, err := s.GetTarget(id)
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	if got.OS != "talos" || got.Arch != "amd64" || got.Mode != "discovery" ||
		got.RetainN != 3 || got.Source != "catalog" || !got.Enabled {
		t.Errorf("GetTarget = %+v, mismatch", got)
	}
}

func TestCreateTarget_UniqueConflict(t *testing.T) {
	s := newTestStore(t)
	base := Target{OS: "talos", Arch: "amd64", Params: "{}", Mode: "manual", Source: "api", Enabled: true}
	if _, err := s.CreateTarget(base); err != nil {
		t.Fatalf("first CreateTarget: %v", err)
	}
	if _, err := s.CreateTarget(base); err == nil {
		t.Error("duplicate (os,arch,params) CreateTarget: err = nil, want UNIQUE error")
	}
}

func TestUpsertTarget_IdempotentOnConflict(t *testing.T) {
	s := newTestStore(t)
	base := Target{OS: "flatcar", Arch: "amd64", Params: "{}", Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true}
	if err := s.UpsertTarget(base); err != nil {
		t.Fatalf("first UpsertTarget: %v", err)
	}
	// Second upsert with same (os,arch,params) must NOT error and must update.
	base.RetainN = 5
	if err := s.UpsertTarget(base); err != nil {
		t.Fatalf("second UpsertTarget: %v", err)
	}
	all, _ := s.ListTargets()
	if len(all) != 1 {
		t.Fatalf("ListTargets = %d, want 1 (upsert not insert)", len(all))
	}
	if all[0].RetainN != 5 {
		t.Errorf("RetainN = %d after upsert, want 5", all[0].RetainN)
	}
}

func TestEnsureTargetCreateIfAbsent(t *testing.T) {
	s := newTestStore(t)
	tgt := Target{OS: "flatcar", Arch: "amd64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true}
	if err := s.EnsureTarget(tgt); err != nil {
		t.Fatalf("EnsureTarget (fresh): %v", err)
	}
	// Simulate an API PATCH: bump retain_n via UpsertTarget.
	rows, _ := s.ListTargets()
	if len(rows) != 1 {
		t.Fatalf("want 1 target, got %d", len(rows))
	}
	patched := rows[0]
	patched.RetainN = 5
	if err := s.UpsertTarget(patched); err != nil {
		t.Fatalf("UpsertTarget: %v", err)
	}
	// A second Ensure with the ORIGINAL retain_n must be a no-op (D1: flag is
	// a first-boot default; the API owns the row thereafter).
	if err := s.EnsureTarget(tgt); err != nil {
		t.Fatalf("EnsureTarget (existing): %v", err)
	}
	rows, _ = s.ListTargets()
	if len(rows) != 1 || rows[0].RetainN != 5 {
		t.Fatalf("EnsureTarget must not clobber: got %+v", rows)
	}
}

func TestUpdateTargetParamsPreservesVersions(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateTarget(Target{OS: "flatcar", Arch: "amd64", Params: "{}", Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: id, Version: "4230.2.2", Source: "discovered", Cached: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTargetParams(id, `{"channel":"stable"}`); err != nil {
		t.Fatalf("UpdateTargetParams: %v", err)
	}
	got, err := s.GetTarget(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Params != `{"channel":"stable"}` {
		t.Fatalf("params = %q, want rewritten in place", got.Params)
	}
	vs, _ := s.ListTargetVersions(id) // same row id → versions preserved
	if len(vs) != 1 || vs[0].Version != "4230.2.2" || !vs[0].Cached {
		t.Fatalf("target_versions must survive the in-place rewrite: %+v", vs)
	}
}

func TestListTargets(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateTarget(Target{OS: "talos", Arch: "amd64", Params: "{}", Mode: "manual", Source: "api", Enabled: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.CreateTarget(Target{OS: "debian", Arch: "amd64", Params: `{"channel":"stable"}`, Mode: "discovery", Source: "api", Enabled: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	all, err := s.ListTargets()
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListTargets returned %d, want 2", len(all))
	}
}
