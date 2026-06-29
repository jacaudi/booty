package db

import "testing"

func TestUpsertAndListTargetVersions(t *testing.T) {
	s := newTestStore(t)
	tid, err := s.CreateTarget(Target{OS: "talos", Arch: "amd64", Params: "{}", Mode: "discovery", Enabled: true})
	if err != nil {
		t.Fatalf("seed target: %v", err)
	}

	if err := s.UpsertTargetVersion(TargetVersion{TargetID: tid, Version: "v1.10.5", Source: "discovered", Cached: false}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Same (target_id,version) updates rather than duplicates.
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: tid, Version: "v1.10.5", Source: "discovered", Cached: true}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := s.ListTargetVersions(tid)
	if err != nil {
		t.Fatalf("ListTargetVersions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d versions, want 1 (upsert, not insert)", len(got))
	}
	if !got[0].Cached {
		t.Errorf("Cached = false after upsert, want true")
	}
}

func TestTargetVersions_CascadeOnTargetDelete(t *testing.T) {
	s := newTestStore(t)
	tid, err := s.CreateTarget(Target{OS: "talos", Arch: "amd64", Params: "{}", Mode: "discovery", Enabled: true})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: tid, Version: "v1.10.5", Source: "discovered"}); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM targets WHERE id = ?`, tid); err != nil {
		t.Fatalf("delete target: %v", err)
	}
	got, err := s.ListTargetVersions(tid)
	if err != nil {
		t.Fatalf("ListTargetVersions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("versions survived target delete: %d (FK cascade not active)", len(got))
	}
}
