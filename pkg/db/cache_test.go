package db

import "testing"

func mustVersionID(t *testing.T, s *Store, targetID int64, version string) int64 {
	t.Helper()
	tvs, err := s.ListTargetVersions(targetID)
	if err != nil {
		t.Fatalf("ListTargetVersions(%d): %v", targetID, err)
	}
	for _, v := range tvs {
		if v.Version == version {
			return v.ID
		}
	}
	t.Fatalf("version %q not found for target %d", version, targetID)
	return 0
}

func TestCacheEntryUpsertAndList(t *testing.T) {
	s := newTestStore(t)
	tgtID, err := s.CreateTarget(Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"abc"}`, Mode: "discovery", RetainN: 3, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: tgtID, Version: "v1.13.5", Source: "discovered", Cached: true}); err != nil {
		t.Fatal(err)
	}
	tvs, _ := s.ListTargetVersions(tgtID)
	tvID := tvs[0].ID

	if err := s.UpsertCacheEntry(tvID, 1234); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListCacheEntries(CacheFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.OS != "talos" || r.Arch != "amd64" || r.Version != "v1.13.5" || r.Size != 1234 || !r.InWindow || r.Pinned {
		t.Fatalf("unexpected joined row: %+v", r)
	}

	// upsert again preserves pinned + updates size; verified stays NULL
	if err := s.SetCachePinned(r.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCacheEntry(tvID, 5678); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListCacheEntries(CacheFilter{})
	if rows[0].Size != 5678 || !rows[0].Pinned {
		t.Fatalf("re-upsert must update size and preserve pinned: %+v", rows[0])
	}

	sum, _ := s.SumCacheBytes()
	if sum != 5678 {
		t.Fatalf("SumCacheBytes want 5678, got %d", sum)
	}
}

func TestCacheEntryArchiveAndCascade(t *testing.T) {
	s := newTestStore(t)
	tgtID, _ := s.CreateTarget(Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"abc"}`, Mode: "discovery", RetainN: 1, Enabled: true})
	_ = s.UpsertTargetVersion(TargetVersion{TargetID: tgtID, Version: "v1.13.5", Source: "discovered", Cached: true})
	tvID := mustVersionID(t, s, tgtID, "v1.13.5")
	_ = s.UpsertCacheEntry(tvID, 100)

	if err := s.SetCacheInWindow(tvID, false); err != nil {
		t.Fatal(err)
	}
	arch, _ := s.ListArchivedUnpinned()
	if len(arch) != 1 || arch[0].InWindow {
		t.Fatalf("want 1 archived-unpinned row, got %+v", arch)
	}

	// deleting the target_version cascades the cache_entries row
	_ = s.DeleteTargetVersion(tgtID, "v1.13.5")
	rows, _ := s.ListCacheEntries(CacheFilter{})
	if len(rows) != 0 {
		t.Fatalf("ON DELETE CASCADE should remove the cache_entries row, got %d", len(rows))
	}
}
