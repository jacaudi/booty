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

func TestDeleteTargetVersion(t *testing.T) {
	s := newTestStore(t)
	tid, err := s.CreateTarget(Target{OS: "talos", Arch: "amd64", Params: "{}", Mode: "discovery", Enabled: true})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: tid, Version: "v1.10.5", Source: "discovered"}); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if err := s.DeleteTargetVersion(tid, "v1.10.5"); err != nil {
		t.Fatalf("DeleteTargetVersion: %v", err)
	}
	got, _ := s.ListTargetVersions(tid)
	if len(got) != 0 {
		t.Errorf("after delete: %d versions, want 0", len(got))
	}
	// Idempotent: deleting an absent (target,version) is a no-op.
	if err := s.DeleteTargetVersion(tid, "v9.9.9"); err != nil {
		t.Errorf("DeleteTargetVersion absent: err = %v, want nil", err)
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

func TestListCachedInWindowVersions(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateTarget(Target{OS: "flatcar", Arch: "amd64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 2, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	seed := func(version string, cached, inWindow bool) {
		t.Helper()
		if err := s.UpsertTargetVersion(TargetVersion{TargetID: id, Version: version, Source: "discovered", Cached: cached}); err != nil {
			t.Fatal(err)
		}
		if cached {
			tvID, err := s.TargetVersionID(id, version)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.UpsertCacheEntry(tvID, 100); err != nil {
				t.Fatal(err)
			}
			if !inWindow {
				if err := s.SetCacheInWindow(tvID, false); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	seed("100.0.0", true, true)  // in-window AND cached -> counts
	seed("99.0.0", true, false)  // archived -> must NOT resurrect
	seed("98.0.0", false, false) // not cached (mid-download / P3b failure row shape) -> must NOT count

	got, err := s.ListCachedInWindowVersions(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "100.0.0" {
		t.Fatalf("ListCachedInWindowVersions = %v, want [100.0.0] only", got)
	}
}
