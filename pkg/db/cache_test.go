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
	tgtID, err := s.CreateTarget(Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"abc"}`, Mode: "discovery", RetainN: 3, Source: "api", Enabled: true})
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

// TestSetCachePinnedByTargetVersion covers the DVD branch's pin path, which
// has a target_version_id but not the cache_entries.id SetCachePinned keys on.
func TestSetCachePinnedByTargetVersion(t *testing.T) {
	s := newTestStore(t)
	tgtID, _ := s.CreateTarget(Target{OS: "debian", Arch: "amd64", Params: `{"channel":"12"}`, Mode: "manual", RetainN: 1, Source: "catalog", Enabled: true})
	_ = s.UpsertTargetVersion(TargetVersion{TargetID: tgtID, Version: "12.15.0", Source: "manual", Cached: true})
	tvID := mustVersionID(t, s, tgtID, "12.15.0")
	_ = s.UpsertCacheEntry(tvID, 100)

	if err := s.SetCachePinnedByTargetVersion(tvID, true); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListCacheEntries(CacheFilter{})
	if len(rows) != 1 || !rows[0].Pinned {
		t.Fatalf("want pinned row, got %+v", rows)
	}
}

// TestCacheEntryExists covers the Debian DVD reconciler's fully-settled
// short-circuit lookup: false before any cache_entries row exists for a
// target_version, true once one has been upserted.
func TestCacheEntryExists(t *testing.T) {
	s := newTestStore(t)
	tgtID, _ := s.CreateTarget(Target{OS: "debian", Arch: "amd64", Params: `{"channel":"12"}`, Mode: "manual", RetainN: 1, Source: "catalog", Enabled: true})
	_ = s.UpsertTargetVersion(TargetVersion{TargetID: tgtID, Version: "12.15.0", Source: "manual", Cached: true})
	tvID := mustVersionID(t, s, tgtID, "12.15.0")

	exists, err := s.CacheEntryExists(tvID)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("no cache_entries row yet: want false")
	}

	if err := s.UpsertCacheEntry(tvID, 100); err != nil {
		t.Fatal(err)
	}
	exists, err = s.CacheEntryExists(tvID)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("cache_entries row now present: want true")
	}
}

func TestCacheEntryArchiveAndCascade(t *testing.T) {
	s := newTestStore(t)
	tgtID, _ := s.CreateTarget(Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"abc"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
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

func seedCacheRow(t *testing.T, s *Store) (targetID, tvID int64) {
	t.Helper()
	var err error
	targetID, err = s.CreateTarget(Target{OS: "flatcar", Arch: "amd64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: targetID, Version: "100.0.0", Source: "discovered", Cached: true}); err != nil {
		t.Fatal(err)
	}
	tvID, err = s.TargetVersionID(targetID, "100.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCacheEntry(tvID, 4096); err != nil {
		t.Fatal(err)
	}
	return targetID, tvID
}

func TestSetCacheVerifiedRoundTrip(t *testing.T) {
	s := newTestStore(t)
	_, tvID := seedCacheRow(t, s)

	// Fresh row: verified is NULL (P3a contract — UpsertCacheEntry never sets it).
	rows, _ := s.ListCacheEntries(CacheFilter{})
	if len(rows) != 1 || rows[0].Verified != nil {
		t.Fatalf("fresh row must read verified=NULL, got %+v", rows)
	}

	no := false
	if err := s.SetCacheVerified(tvID, &no, "checksum mismatch"); err != nil {
		t.Fatalf("SetCacheVerified false: %v", err)
	}
	rows, _ = s.ListCacheEntries(CacheFilter{})
	if rows[0].Verified == nil || *rows[0].Verified || rows[0].VerifyErr != "checksum mismatch" {
		t.Fatalf("want verified=false + err, got %+v", rows[0])
	}

	// nil clears back to NULL (a reverify of a zero-verifiable version).
	if err := s.SetCacheVerified(tvID, nil, ""); err != nil {
		t.Fatalf("SetCacheVerified nil: %v", err)
	}
	rows, _ = s.ListCacheEntries(CacheFilter{})
	if rows[0].Verified != nil {
		t.Fatalf("nil must clear verified to NULL, got %+v", rows[0])
	}
}

func TestUpsertCacheEntryNeverClobbersVerified(t *testing.T) {
	s := newTestStore(t)
	_, tvID := seedCacheRow(t, s)
	yes := true
	if err := s.SetCacheVerified(tvID, &yes, ""); err != nil {
		t.Fatal(err)
	}
	// A later reconcile re-upserts size — verified must survive (P3a regression guard).
	if err := s.UpsertCacheEntry(tvID, 8192); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListCacheEntries(CacheFilter{})
	if rows[0].Verified == nil || !*rows[0].Verified || rows[0].Size != 8192 {
		t.Fatalf("UpsertCacheEntry must preserve verified and update size, got %+v", rows[0])
	}
}

func TestUpsertCacheEntryArchivedWritesFailureRow(t *testing.T) {
	s := newTestStore(t)
	_, tvID := seedCacheRow(t, s)
	if err := s.UpsertCacheEntryArchived(tvID, "signature mismatch"); err != nil {
		t.Fatalf("UpsertCacheEntryArchived: %v", err)
	}
	rows, _ := s.ListCacheEntries(CacheFilter{})
	r := rows[0]
	if r.InWindow || r.Size != 0 || r.Verified == nil || *r.Verified || r.VerifyErr != "signature mismatch" {
		t.Fatalf("failure row must be in_window=0 size=0 verified=0 + err, got %+v", r)
	}
}

func TestListArchivedUnpinnedExcludesZeroByteRows(t *testing.T) {
	s := newTestStore(t)
	_, tvID := seedCacheRow(t, s)
	if err := s.SetCacheInWindow(tvID, false); err != nil { // archive the real (size>0) row
		t.Fatal(err)
	}
	// A zero-byte failure row on a second version.
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: 1, Version: "99.0.0", Source: "discovered"}); err != nil {
		t.Fatal(err)
	}
	tv2, _ := s.TargetVersionID(1, "99.0.0")
	if err := s.UpsertCacheEntryArchived(tv2, "signature mismatch"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListArchivedUnpinned()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Version != "100.0.0" {
		t.Fatalf("size=0 failure row must be excluded from eviction candidates, got %+v", got)
	}
}

func TestUpsertCacheEntryArchivedFreshInsert(t *testing.T) {
	s := newTestStore(t)
	targetID, err := s.CreateTarget(Target{OS: "flatcar", Arch: "amd64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: targetID, Version: "100.0.0", Source: "discovered", Cached: true}); err != nil {
		t.Fatal(err)
	}
	tvID, err := s.TargetVersionID(targetID, "100.0.0")
	if err != nil {
		t.Fatal(err)
	}

	// No UpsertCacheEntry call: bytes never landed, so there is no cache_entries
	// row yet — this exercises the fresh-INSERT branch, not ON CONFLICT.
	if err := s.UpsertCacheEntryArchived(tvID, "gpg: signature mismatch"); err != nil {
		t.Fatalf("UpsertCacheEntryArchived: %v", err)
	}
	rows, _ := s.ListCacheEntries(CacheFilter{})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.InWindow || r.Size != 0 || r.Verified == nil || *r.Verified || r.VerifyErr != "gpg: signature mismatch" || r.Pinned {
		t.Fatalf("fresh-insert failure row must be in_window=0 size=0 verified=false pinned=false + err, got %+v", r)
	}
}
