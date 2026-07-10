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

func TestPinManualVersionPreservesCached(t *testing.T) {
	s := newTestStore(t)
	tid, err := s.CreateTarget(Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"s"}`, Mode: "discovery", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// A discovered, already-cached version.
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: tid, Version: "v1.11.0", Source: "discovered", Cached: true}); err != nil {
		t.Fatal(err)
	}
	// Manually pinning it must flip source to 'manual' WITHOUT resetting cached
	// (which would force a needless re-download).
	if err := s.PinManualVersion(tid, "v1.11.0"); err != nil {
		t.Fatal(err)
	}
	vs, err := s.ListTargetVersions(tid)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, v := range vs {
		if v.Version == "v1.11.0" {
			found = true
			if v.Source != "manual" {
				t.Errorf("source = %q, want manual", v.Source)
			}
			if !v.Cached {
				t.Error("pinning reset cached to false (would force a needless re-download)")
			}
		}
	}
	if !found {
		t.Fatal("pinned version missing")
	}
	// A pin on a brand-new version starts uncached.
	if err := s.PinManualVersion(tid, "v1.9.0"); err != nil {
		t.Fatal(err)
	}
	vs, _ = s.ListTargetVersions(tid)
	for _, v := range vs {
		if v.Version == "v1.9.0" && (v.Source != "manual" || v.Cached) {
			t.Fatalf("new manual pin = %+v, want source=manual cached=false", v)
		}
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

	// A manual pin, cached and in-window, must NOT consume a window slot: it is
	// always desired and never archived by the prune loop, so counting it would
	// only displace a discovered version.
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: id, Version: "200.0.0", Source: "manual", Cached: true}); err != nil {
		t.Fatal(err)
	}
	manualTvID, err := s.TargetVersionID(id, "200.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCacheEntry(manualTvID, 100); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListCachedInWindowVersions(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "100.0.0" {
		t.Fatalf("ListCachedInWindowVersions = %v, want [100.0.0] only (manual pin excluded)", got)
	}
}
