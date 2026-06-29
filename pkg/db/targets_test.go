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
		Mode: "discovery", RetainN: 3, Predefined: true, Enabled: true,
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
		got.RetainN != 3 || !got.Predefined || !got.Enabled {
		t.Errorf("GetTarget = %+v, mismatch", got)
	}
}

func TestCreateTarget_UniqueConflict(t *testing.T) {
	s := newTestStore(t)
	base := Target{OS: "talos", Arch: "amd64", Params: "{}", Mode: "manual", Enabled: true}
	if _, err := s.CreateTarget(base); err != nil {
		t.Fatalf("first CreateTarget: %v", err)
	}
	if _, err := s.CreateTarget(base); err == nil {
		t.Error("duplicate (os,arch,params) CreateTarget: err = nil, want UNIQUE error")
	}
}

func TestListTargets(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateTarget(Target{OS: "talos", Arch: "amd64", Params: "{}", Mode: "manual", Enabled: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.CreateTarget(Target{OS: "debian", Arch: "amd64", Params: `{"channel":"stable"}`, Mode: "discovery", Enabled: true}); err != nil {
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
