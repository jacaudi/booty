package db

import (
	"testing"
	"time"
)

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

// seedRejectedVersion creates a target + version and writes the rejection row
// UpsertCacheEntryArchived produces: size=0, in_window=0, verified=0,
// verify_err non-empty. Returns (targetID, target_version id).
func seedRejectedVersion(t *testing.T, s *Store, os_, arch, version, verifyErr string) (int64, int64) {
	t.Helper()
	tid, err := s.CreateTarget(Target{
		OS: os_, Arch: arch, Params: "{}", Mode: "discovery", RetainN: 1, Source: "api", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: tid, Version: version, Source: "discovered"}); err != nil {
		t.Fatalf("UpsertTargetVersion: %v", err)
	}
	tvID := mustVersionID(t, s, tid, version)
	if err := s.UpsertCacheEntryArchived(tvID, verifyErr); err != nil {
		t.Fatalf("UpsertCacheEntryArchived: %v", err)
	}
	return tid, tvID
}

// The in-window arm. This is ALSO the only test that detects the NULL-modifier
// bug: with `-1h0m0s`, datetime() returns NULL, `fetched_at > NULL` is NULL, the
// WHERE never matches, and blocked would be false here while every other arm
// still passed.
func TestVerifyRejectedWithinBlocksAFreshRejection(t *testing.T) {
	s := newTestStore(t)
	tid, _ := seedRejectedVersion(t, s, "tails", "amd64", "7.10-17629562", "tails-amd64.iso: checksum mismatch")

	blocked, verifyErr, err := s.VerifyRejectedWithin(tid, "7.10-17629562", time.Hour)
	if err != nil {
		t.Fatalf("VerifyRejectedWithin: %v", err)
	}
	if !blocked {
		t.Fatal("a rejection written moments ago must be BLOCKED within a 1h window " +
			"(if this is the only failing arm, the SQLite modifier is producing NULL)")
	}
	if verifyErr != "tails-amd64.iso: checksum mismatch" {
		t.Errorf("verifyErr = %q, want the recorded rejection message", verifyErr)
	}
}

// Once the rejection ages past the window the version must be retried.
func TestVerifyRejectedWithinReleasesAnAgedRejection(t *testing.T) {
	s := newTestStore(t)
	tid, tvID := seedRejectedVersion(t, s, "tails", "amd64", "7.10-17629562", "boom")

	// Age the row. fetched_at is a UTC datetime() TEXT string, so age it with
	// SQL rather than a Go-side time format.
	if _, err := s.db.Exec(
		`UPDATE cache_entries SET fetched_at = datetime('now','-2 hours') WHERE target_version_id = ?`, tvID); err != nil {
		t.Fatalf("age row: %v", err)
	}

	blocked, _, err := s.VerifyRejectedWithin(tid, "7.10-17629562", time.Hour)
	if err != nil {
		t.Fatalf("VerifyRejectedWithin: %v", err)
	}
	if blocked {
		t.Fatal("a rejection older than the window must NOT block; the guard would never self-clear")
	}
}

// The four-column predicate exists to exclude this exact shape. A warn-landed
// failed version (cached=1, size>0, in_window=1) whose files later go missing
// keeps a STALE verified=0/verify_err while cached drops to 0. A transport
// error on the re-download writes nothing, leaving that two-column signature —
// which a two-column predicate would misread as a verification rejection and
// guard, breaking the promise that transport failures are never guarded.
func TestVerifyRejectedWithinIgnoresAWarnLandedRowWithBytes(t *testing.T) {
	s := newTestStore(t)
	tid, err := s.CreateTarget(Target{
		OS: "flatcar", Arch: "amd64", Params: `{"channel":"stable"}`,
		Mode: "discovery", RetainN: 1, Source: "api", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: tid, Version: "4152.2.0", Source: "discovered", Cached: true}); err != nil {
		t.Fatalf("UpsertTargetVersion: %v", err)
	}
	tvID := mustVersionID(t, s, tid, "4152.2.0")
	// Warn-landed: real bytes recorded (size>0, in_window=1), then a failure verdict.
	if err := s.UpsertCacheEntry(tvID, 4096); err != nil {
		t.Fatalf("UpsertCacheEntry: %v", err)
	}
	if err := s.SetCacheVerified(tvID, new(false), "kernel: checksum mismatch"); err != nil {
		t.Fatalf("SetCacheVerified: %v", err)
	}
	// Simulate the later cached=0 drop that versions.go's `cached = excluded.cached` performs.
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: tid, Version: "4152.2.0", Source: "discovered", Cached: false}); err != nil {
		t.Fatalf("UpsertTargetVersion: %v", err)
	}

	blocked, _, err := s.VerifyRejectedWithin(tid, "4152.2.0", time.Hour)
	if err != nil {
		t.Fatalf("VerifyRejectedWithin: %v", err)
	}
	if blocked {
		t.Fatal("a warn-landed row with bytes on disk is NOT a verification rejection; " +
			"guarding it would break the promise that transport failures are never guarded")
	}
}

// No cache_entries row at all — the normal state for a version that has never
// been attempted. Must be "not guarded" and must not error.
func TestVerifyRejectedWithinIsFalseWithNoRow(t *testing.T) {
	s := newTestStore(t)
	tid, err := s.CreateTarget(Target{
		OS: "talos", Arch: "amd64", Params: `{"schematic":"abc"}`,
		Mode: "discovery", RetainN: 1, Source: "api", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := s.UpsertTargetVersion(TargetVersion{TargetID: tid, Version: "v1.9.0", Source: "discovered"}); err != nil {
		t.Fatalf("UpsertTargetVersion: %v", err)
	}

	blocked, verifyErr, err := s.VerifyRejectedWithin(tid, "v1.9.0", time.Hour)
	if err != nil {
		t.Fatalf("a missing row must not be an error: %v", err)
	}
	if blocked || verifyErr != "" {
		t.Fatalf("blocked=%v verifyErr=%q, want false/\"\"", blocked, verifyErr)
	}
	// An unknown version behaves identically.
	blocked, _, err = s.VerifyRejectedWithin(tid, "v9.9.9", time.Hour)
	if err != nil || blocked {
		t.Fatalf("unknown version: blocked=%v err=%v, want false/nil", blocked, err)
	}
}

// A zero (or negative) window disables the guard rather than blocking forever.
// Task 7's integration test relies on this to prove the release arm without sleeping.
func TestVerifyRejectedWithinZeroWindowNeverBlocks(t *testing.T) {
	s := newTestStore(t)
	tid, _ := seedRejectedVersion(t, s, "tails", "amd64", "7.10-17629562", "boom")

	blocked, _, err := s.VerifyRejectedWithin(tid, "7.10-17629562", 0)
	if err != nil {
		t.Fatalf("VerifyRejectedWithin: %v", err)
	}
	if blocked {
		t.Fatal("a zero window must disable the guard, not block")
	}
}
