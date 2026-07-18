package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// newTestPGPEntity generates a throwaway keypair for signing test fixtures.
func newTestPGPEntity(t *testing.T) *openpgp.Entity {
	t.Helper()
	ent, err := openpgp.NewEntity("debian-dvd-test", "", "t@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	return ent
}

// writeDetachedSig binary-detach-signs signedPath with ent and writes the
// signature to sigPath, mirroring the on-disk SHA256SUMS.sign layout.
func writeDetachedSig(t *testing.T, signedPath, sigPath string, ent *openpgp.Entity) {
	t.Helper()
	body, err := os.ReadFile(signedPath)
	if err != nil {
		t.Fatal(err)
	}
	var sig bytes.Buffer
	if err := openpgp.DetachSign(&sig, ent, bytes.NewReader(body), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sigPath, sig.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// swapDebianKeyring injects a fixture keyring in place of the embedded Debian
// CD key for the duration of a test, returning a restore func.
func swapDebianKeyring(key []byte) func() {
	old := debianCDKeyring
	debianCDKeyring = key
	return func() { debianCDKeyring = old }
}

// fakeExtractor returns an isoExtractFunc that, keyed on the ISO's base
// filename, lays down a fixture tree of relative-path -> file-content pairs
// instead of reading a real ISO9660 image. Used by the merge-logic test so it
// needs no real ISO (task-6-brief.md Step 1).
func fakeExtractor(discs map[string]map[string]string) isoExtractFunc {
	return func(_ context.Context, isoPath, destDir string) error {
		files, ok := discs[filepath.Base(isoPath)]
		if !ok {
			return fmt.Errorf("fake extractor: no fixture for %s", isoPath)
		}
		for rel, content := range files {
			full := filepath.Join(destDir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
}

// swapISOExtractor injects a fake isoExtractFunc in place of the real
// diskfs-based extractor for the duration of a test, returning a restore
// func. Mirrors swapDebianKeyring above (same seam strategy).
func swapISOExtractor(fn isoExtractFunc) func() {
	old := isoExtractor
	isoExtractor = fn
	return func() { isoExtractor = old }
}

// assertFile fails the test unless path contains exactly want.
func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func TestExtractAndMergeISO_PoolUnionDisc1Dists(t *testing.T) {
	// fake per-disc extracted contents
	disc1 := map[string]string{
		"dists/bookworm/main/binary-amd64/Packages": "Package: a\n",
		"pool/main/a/a/a_1.deb":                      "AAA",
		"install.amd64/linux":                        "KERNEL",
	}
	disc2 := map[string]string{
		"dists/bookworm/main/binary-amd64/Packages": "SHOULD-NOT-WIN\n", // disc-2 dists ignored
		"pool/main/b/b/b_1.deb":                      "BBB",              // unioned in
	}
	restore := swapISOExtractor(fakeExtractor(map[string]map[string]string{
		"DVD-1.iso": disc1, "DVD-2.iso": disc2,
	}))
	defer restore()

	isoDir := t.TempDir() // where the verbatim ISOs would live (fake extractor keys on name)
	final := filepath.Join(t.TempDir(), "12", "amd64", "12.15.0")
	err := extractAndMergeISO(t.Context(),
		isoDir, []string{"DVD-1.iso", "DVD-2.iso"}, final, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	// disc-1 dists verbatim
	assertFile(t, filepath.Join(final, "dists/bookworm/main/binary-amd64/Packages"), "Package: a\n")
	// pool union
	assertFile(t, filepath.Join(final, "pool/main/a/a/a_1.deb"), "AAA")
	assertFile(t, filepath.Join(final, "pool/main/b/b/b_1.deb"), "BBB")
	// install tree present (boot source)
	assertFile(t, filepath.Join(final, "install.amd64/linux"), "KERNEL")
	// sentinel written
	if !dvdSentinelPresent(final) {
		t.Fatal("extraction sentinel must exist after a successful merge")
	}
}

func TestVerifyDVDChecksums_HappyAndTamper(t *testing.T) {
	dir := t.TempDir()
	iso := []byte("pretend-iso-contents")
	if err := os.WriteFile(filepath.Join(dir, "debian-12.15.0-amd64-DVD-1.iso"), iso, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(iso)
	sums := fmt.Sprintf("%x  debian-12.15.0-amd64-DVD-1.iso\n", sum[:])
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	// sign SHA256SUMS with a throwaway entity; write SHA256SUMS.sign; inject the
	// entity's public key via the keyring seam.
	ent := newTestPGPEntity(t)
	writeDetachedSig(t, filepath.Join(dir, "SHA256SUMS"), filepath.Join(dir, "SHA256SUMS.sign"), ent)
	restore := swapDebianKeyring(armorPublicKey(t, ent))
	defer restore()

	names := []string{"debian-12.15.0-amd64-DVD-1.iso"}
	if err := verifyDVDChecksums(t.Context(), dir, names); err != nil {
		t.Fatalf("valid set should verify: %v", err)
	}
	// tamper the ISO -> checksum mismatch
	if err := os.WriteFile(filepath.Join(dir, names[0]), append(iso, 'X'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyDVDChecksums(t.Context(), dir, names); err == nil {
		t.Fatal("tampered ISO must fail checksum")
	}
}

// swapDVDSeams injects fakes for the three isoDownload/isoVerify/isoExtract
// package-var seams for the duration of a test (same strategy as
// swapDebianKeyring/swapISOExtractor above), returning them via t.Cleanup.
func swapDVDSeams(t *testing.T,
	download func(context.Context, string, string) error,
	verify func(context.Context, string, []string) error,
	extract func(context.Context, string, []string, string, string) error) {
	t.Helper()
	od, ov, oe := isoDownload, isoVerify, isoExtract
	isoDownload, isoVerify, isoExtract = download, verify, extract
	t.Cleanup(func() { isoDownload, isoVerify, isoExtract = od, ov, oe })
}

// newEnsureDVDStore sets DataDir to a fresh t.TempDir() (cacheDir resolves
// under it) and opens a temp SQLite store, mirroring the fixture pattern used
// throughout reconcile_test.go.
func newEnsureDVDStore(t *testing.T) *db.Store {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	store, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestEnsureDebianDVD_PromoteFlipsPinsAndIsIdempotent(t *testing.T) {
	store := newEnsureDVDStore(t)
	var downloads int
	swapDVDSeams(t,
		func(ctx context.Context, url, dest string) error { downloads++; return os.WriteFile(dest, []byte("iso"), 0o644) },
		func(ctx context.Context, dir string, names []string) error { return nil },
		func(ctx context.Context, isoDir string, names []string, final, arch string) error {
			if err := os.MkdirAll(final, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(final, dvdSentinelName), nil, 0o644) // sentinel
		})

	id, _ := store.CreateTarget(db.Target{OS: "debian", Arch: "amd64", Params: `{"channel":"12"}`,
		Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true, SourceMode: "netinst", DvdCount: 1})
	_ = store.SetTargetDesiredMode(id, "dvd", 1)

	// Seed a prior netinst-cached version (row + cache_entries + on-disk dir) that
	// the DVD tree supersedes; after promotion it must be fully reconciled away
	// (dir removed AND row/cache_entries deleted), not left as an orphaned
	// cached=1/size>0 row that permanently overcounts SumCacheBytes.
	_ = store.UpsertTargetVersion(db.TargetVersion{TargetID: id, Version: "12.14.0", Source: "discovered", Cached: true})
	oldTvID, _ := store.TargetVersionID(id, "12.14.0")
	_ = store.UpsertCacheEntry(oldTvID, 999)
	if err := os.MkdirAll(cacheDir("debian", "12", "amd64", "12.14.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	tgt, _ := store.GetTarget(id)

	if err := ensureDebianDVD(t.Context(), store, *tgt, "12.15.0"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetTarget(id)
	if got.SourceMode != "dvd" || got.DesiredMode != "" {
		t.Fatalf("post-promote: source=%q desired=%q, want dvd/empty", got.SourceMode, got.DesiredMode)
	}
	rows, _ := store.ListCacheEntries(db.CacheFilter{OS: "debian"})
	if len(rows) != 1 || !rows[0].Pinned || rows[0].Version != "12.15.0" {
		t.Fatalf("want 1 pinned cache entry for 12.15.0 (never-evict §11.2), got %+v", rows)
	}
	// Superseded netinst version fully gone from the DB (dir + row + cache_entries).
	tvs, _ := store.ListTargetVersions(id)
	for _, v := range tvs {
		if v.Version == "12.14.0" {
			t.Fatalf("superseded netinst version 12.14.0 must be deleted, still present: %+v", tvs)
		}
	}
	if cacheDirExists("debian", "12", "amd64", "12.14.0") {
		t.Fatal("superseded netinst version dir must be removed from disk")
	}
	downloads = 0 // idempotent: sentinel present → no re-download
	if err := ensureDebianDVD(t.Context(), store, *got, "12.15.0"); err != nil {
		t.Fatal(err)
	}
	if downloads != 0 {
		t.Fatalf("second ensure re-downloaded %d files; sentinel must short-circuit", downloads)
	}
}

// TestEnsureDebianDVD_SentinelPresentButRowsMissingSelfHeals covers the crash
// window between isoExtract writing the sentinel and the DB mutations landing:
// if a prior tick died there (or a transient SQLITE_BUSY aborted the writes),
// the sentinel is on disk but the target_versions/cache_entries rows and the
// source_mode flip never happened. The next tick must SELF-HEAL — record the
// (idempotent) rows, pin, and flip — WITHOUT re-downloading (the sentinel
// short-circuits the heavy work).
func TestEnsureDebianDVD_SentinelPresentButRowsMissingSelfHeals(t *testing.T) {
	store := newEnsureDVDStore(t)
	var downloads int
	swapDVDSeams(t,
		func(ctx context.Context, url, dest string) error { downloads++; return os.WriteFile(dest, []byte("iso"), 0o644) },
		func(ctx context.Context, dir string, names []string) error { return nil },
		func(ctx context.Context, isoDir string, names []string, final, arch string) error { return nil })

	id, _ := store.CreateTarget(db.Target{OS: "debian", Arch: "amd64", Params: `{"channel":"12"}`,
		Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true, SourceMode: "netinst", DvdCount: 1})
	_ = store.SetTargetDesiredMode(id, "dvd", 1)
	tgt, _ := store.GetTarget(id)

	// Pre-create the version dir WITH the completion sentinel but NO rows —
	// simulating an interrupted prior run (sentinel written, DB writes lost).
	dir := cacheDir("debian", "12", "amd64", "12.15.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, dvdSentinelName), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureDebianDVD(t.Context(), store, *tgt, "12.15.0"); err != nil {
		t.Fatal(err)
	}
	if downloads != 0 {
		t.Fatalf("sentinel present must skip ALL downloads; got %d", downloads)
	}
	got, _ := store.GetTarget(id)
	if got.SourceMode != "dvd" || got.DesiredMode != "" {
		t.Fatalf("self-heal: source=%q desired=%q, want dvd/empty", got.SourceMode, got.DesiredMode)
	}
	rows, _ := store.ListCacheEntries(db.CacheFilter{OS: "debian"})
	if len(rows) != 1 || !rows[0].Pinned {
		t.Fatalf("self-heal must create the pinned cache entry, got %+v", rows)
	}
}

// TestEnsureDebianDVD_VerifyFailureClearsISOsForRefetch guards against a stall
// introduced by the NEW-1 skip-already-downloaded optimization: a full-size but
// wrong-content ISO (e.g. a divergent mirror / re-spun point release) fails
// isoVerify, but isoVerify only DETECTS the mismatch — it doesn't remove the bad
// file. Without cleanup, the next tick would os.Stat the still-present destPath,
// SKIP the re-download, and fail verify again forever. So on a verify failure
// ensureDebianDVD must REMOVE the downloaded ISOs (+ SHA256SUMS/.sign) before
// returning the error, letting the next tick re-download clean. This is still a
// pre-flip failure path: source_mode stays netinst, desired_mode stays dvd, and
// no extracted tree exists yet.
func TestEnsureDebianDVD_VerifyFailureClearsISOsForRefetch(t *testing.T) {
	store := newEnsureDVDStore(t)
	swapDVDSeams(t,
		func(ctx context.Context, url, dest string) error { return os.WriteFile(dest, []byte("bad"), 0o644) },
		func(ctx context.Context, dir string, names []string) error { return errors.New("checksum mismatch") },
		func(ctx context.Context, isoDir string, names []string, final, arch string) error { return nil })

	id, _ := store.CreateTarget(db.Target{OS: "debian", Arch: "amd64", Params: `{"channel":"12"}`,
		Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true, SourceMode: "netinst", DvdCount: 2})
	_ = store.SetTargetDesiredMode(id, "dvd", 2)
	tgt, _ := store.GetTarget(id)

	if err := ensureDebianDVD(t.Context(), store, *tgt, "12.15.0"); err == nil {
		t.Fatal("expected verify failure")
	}

	// Every ISO plus SHA256SUMS/.sign must be gone so the next tick re-downloads
	// them clean (the skip-if-destPath-exists path would otherwise loop forever).
	dir := cacheDir("debian", "12", "amd64", "12.15.0")
	isoNames, _, _, _ := debianDVDSources("12", "amd64", "12.15.0", 2)
	for _, name := range append(isoNames, "SHA256SUMS", "SHA256SUMS.sign") {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("verify-rejected %s must be removed for a clean re-fetch; still present (err=%v)", name, err)
		}
	}

	// Still a pre-flip failure: netinst preserved, desired_mode still dvd.
	got, _ := store.GetTarget(id)
	if got.SourceMode != "netinst" || got.DesiredMode != "dvd" {
		t.Fatalf("verify failure must leave source=netinst, desired=dvd; got %q/%q", got.SourceMode, got.DesiredMode)
	}
}

func TestEnsureDebianDVD_FailedDownloadLeavesNetinst(t *testing.T) {
	store := newEnsureDVDStore(t)
	swapDVDSeams(t,
		func(ctx context.Context, url, dest string) error { return errors.New("boom") },
		func(ctx context.Context, dir string, names []string) error { return nil },
		func(ctx context.Context, isoDir string, names []string, final, arch string) error { return nil })

	id, _ := store.CreateTarget(db.Target{OS: "debian", Arch: "amd64", Params: `{"channel":"12"}`,
		Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true, SourceMode: "netinst", DvdCount: 1})
	_ = store.SetTargetDesiredMode(id, "dvd", 1)
	tgt, _ := store.GetTarget(id)
	if err := ensureDebianDVD(t.Context(), store, *tgt, "12.15.0"); err == nil {
		t.Fatal("expected download failure")
	}
	got, _ := store.GetTarget(id)
	if got.SourceMode != "netinst" || got.DesiredMode != "dvd" {
		t.Fatalf("failure must leave source=netinst, desired=dvd (retry next tick); got %q/%q", got.SourceMode, got.DesiredMode)
	}
}

// TestExistingDVDVersion covers the reconciler's network-avoidance lookup
// (NEW-6): it must find a target's already-settled DVD version — a manual,
// cached target_versions row whose on-disk dir still carries the completion
// sentinel — WITHOUT any network access (no ctx is even accepted), and must
// report ok=false for every case that requires a fresh discovery instead:
// no rows at all, a discovered (non-manual) row, an uncached manual row, and
// a manual+cached row whose dir lost its sentinel.
func TestExistingDVDVersion(t *testing.T) {
	store := newEnsureDVDStore(t)
	id, _ := store.CreateTarget(db.Target{OS: "debian", Arch: "amd64", Params: `{"channel":"12"}`,
		Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true, SourceMode: "netinst", DvdCount: 1})
	tgt, _ := store.GetTarget(id)

	if _, ok := existingDVDVersion(store, *tgt); ok {
		t.Fatal("no rows yet: want ok=false")
	}

	// A discovered (not manual) row must be ignored even if cached.
	_ = store.UpsertTargetVersion(db.TargetVersion{TargetID: id, Version: "12.14.0", Source: "discovered", Cached: true})
	if _, ok := existingDVDVersion(store, *tgt); ok {
		t.Fatal("discovered row must not count as a settled DVD version")
	}

	// A manual but uncached row (e.g. mid-promote, before ensureDebianDVD ever
	// ran) must be ignored.
	_ = store.PinManualVersion(id, "12.15.0")
	if _, ok := existingDVDVersion(store, *tgt); ok {
		t.Fatal("manual+uncached row must not count as a settled DVD version")
	}

	// Manual + cached, but the on-disk sentinel is missing (a stale DB row
	// outliving its tree) — must not be trusted either.
	_ = store.UpsertTargetVersion(db.TargetVersion{TargetID: id, Version: "12.15.0", Source: "manual", Cached: true})
	if _, ok := existingDVDVersion(store, *tgt); ok {
		t.Fatal("manual+cached row with no on-disk sentinel must not count as settled")
	}

	// Write the sentinel: now it must resolve, with no network involved.
	dir := cacheDir("debian", "12", "amd64", "12.15.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, dvdSentinelName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	version, ok := existingDVDVersion(store, *tgt)
	if !ok || version != "12.15.0" {
		t.Fatalf("want settled version 12.15.0/true, got %q/%v", version, ok)
	}
}

// TestEnsureDebianDVD_FullySettledIsNoOp covers NEW-4: once a DVD target is
// fully settled (sentinel present, source_mode=dvd, cache_entries row
// recorded), a second ensureDebianDVD call for the SAME version must be a
// true no-op — no downloads, and no re-walk/re-upsert of the DB row. Proven
// by mutating the on-disk tree between calls (adding a file that would
// change dirSize if re-walked) and asserting the recorded size is untouched.
func TestEnsureDebianDVD_FullySettledIsNoOp(t *testing.T) {
	store := newEnsureDVDStore(t)
	var downloads int
	swapDVDSeams(t,
		func(ctx context.Context, url, dest string) error { downloads++; return os.WriteFile(dest, []byte("iso"), 0o644) },
		func(ctx context.Context, dir string, names []string) error { return nil },
		func(ctx context.Context, isoDir string, names []string, final, arch string) error {
			if err := os.MkdirAll(final, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(final, dvdSentinelName), nil, 0o644)
		})

	id, _ := store.CreateTarget(db.Target{OS: "debian", Arch: "amd64", Params: `{"channel":"12"}`,
		Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true, SourceMode: "netinst", DvdCount: 1})
	_ = store.SetTargetDesiredMode(id, "dvd", 1)
	tgt, _ := store.GetTarget(id)

	if err := ensureDebianDVD(t.Context(), store, *tgt, "12.15.0"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetTarget(id) // now source_mode == "dvd"
	rowsBefore, _ := store.ListCacheEntries(db.CacheFilter{OS: "debian"})
	if len(rowsBefore) != 1 {
		t.Fatalf("want 1 cache entry after first ensure, got %d", len(rowsBefore))
	}
	sizeBefore := rowsBefore[0].Size

	// Mutate the on-disk tree: if the second call re-walks it, the recorded
	// size will change; if it's a true no-op, it won't.
	dir := cacheDir("debian", "12", "amd64", "12.15.0")
	if err := os.WriteFile(filepath.Join(dir, "extra.bin"), []byte("EXTRA-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}

	downloads = 0
	if err := ensureDebianDVD(t.Context(), store, *got, "12.15.0"); err != nil {
		t.Fatal(err)
	}
	if downloads != 0 {
		t.Fatalf("fully-settled target must not download: got %d", downloads)
	}
	rowsAfter, _ := store.ListCacheEntries(db.CacheFilter{OS: "debian"})
	if len(rowsAfter) != 1 || rowsAfter[0].Size != sizeBefore {
		t.Fatalf("fully-settled short-circuit must not re-walk/re-upsert: before=%d after=%+v", sizeBefore, rowsAfter)
	}
}

// TestEnsureDebianDVD_RemovesStaleNetinstArtifactsForSameVersion covers
// NEW-5: when the DVD version's dir also happens to be the version a prior
// netinst caching pass used, the bare linux/initrd.gz netboot files left
// behind must be removed once the DVD tree is settled — superseded by
// install.<arch>/ inside the merged tree (design §8.5).
func TestEnsureDebianDVD_RemovesStaleNetinstArtifactsForSameVersion(t *testing.T) {
	store := newEnsureDVDStore(t)
	swapDVDSeams(t,
		func(ctx context.Context, url, dest string) error { return os.WriteFile(dest, []byte("iso"), 0o644) },
		func(ctx context.Context, dir string, names []string) error { return nil },
		func(ctx context.Context, isoDir string, names []string, final, arch string) error {
			if err := os.MkdirAll(filepath.Join(final, "install."+arch), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(final, "install."+arch, "linux"), []byte("K"), 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(final, dvdSentinelName), nil, 0o644)
		})

	id, _ := store.CreateTarget(db.Target{OS: "debian", Arch: "amd64", Params: `{"channel":"13"}`,
		Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true, SourceMode: "netinst", DvdCount: 1})
	_ = store.SetTargetDesiredMode(id, "dvd", 1)
	tgt, _ := store.GetTarget(id)

	// Same version's dir already carries netinst artifacts (a prior netinst
	// cache pass for the same point release before it was promoted).
	dir := cacheDir("debian", "13", "amd64", "13.1.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "linux"), []byte("OLD-KERNEL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "initrd.gz"), []byte("OLD-INITRD"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureDebianDVD(t.Context(), store, *tgt, "13.1.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "linux")); !os.IsNotExist(err) {
		t.Fatal("stale bare netinst linux must be removed after DVD extract for the same version")
	}
	if _, err := os.Stat(filepath.Join(dir, "initrd.gz")); !os.IsNotExist(err) {
		t.Fatal("stale bare netinst initrd.gz must be removed after DVD extract for the same version")
	}
	// install.<arch>/ tree (the DVD's own boot source) must be untouched.
	if _, err := os.Stat(filepath.Join(dir, "install.amd64", "linux")); err != nil {
		t.Fatalf("install.amd64/linux must remain: %v", err)
	}
}

// TestEnsureDebianDVD_SkipsAlreadyDownloadedISO covers NEW-1: an ISO whose
// final destPath already exists on disk (a completed disc from a prior,
// interrupted attempt — e.g. disc-2 of a multi-disc set failed, or extract
// failed after every disc landed) must not be re-downloaded on retry.
func TestEnsureDebianDVD_SkipsAlreadyDownloadedISO(t *testing.T) {
	store := newEnsureDVDStore(t)
	var downloadedURLs []string
	swapDVDSeams(t,
		func(ctx context.Context, url, dest string) error {
			downloadedURLs = append(downloadedURLs, url)
			return os.WriteFile(dest, []byte("data"), 0o644)
		},
		func(ctx context.Context, dir string, names []string) error { return nil },
		func(ctx context.Context, isoDir string, names []string, final, arch string) error {
			if err := os.MkdirAll(final, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(final, dvdSentinelName), nil, 0o644)
		})

	id, _ := store.CreateTarget(db.Target{OS: "debian", Arch: "amd64", Params: `{"channel":"12"}`,
		Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true, SourceMode: "netinst", DvdCount: 2})
	_ = store.SetTargetDesiredMode(id, "dvd", 2)
	tgt, _ := store.GetTarget(id)

	dir := cacheDir("debian", "12", "amd64", "12.15.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	isoNames, _, _, _ := debianDVDSources("12", "amd64", "12.15.0", 2)
	if err := os.WriteFile(filepath.Join(dir, isoNames[0]), []byte("already-here"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureDebianDVD(t.Context(), store, *tgt, "12.15.0"); err != nil {
		t.Fatal(err)
	}
	for _, u := range downloadedURLs {
		if strings.Contains(u, isoNames[0]) {
			t.Fatalf("already-downloaded disc-1 must not be re-downloaded; got download of %s", u)
		}
	}
	found := false
	for _, u := range downloadedURLs {
		if strings.Contains(u, isoNames[1]) {
			found = true
		}
	}
	if !found {
		t.Fatalf("disc-2 must still be downloaded; got %v", downloadedURLs)
	}
	body, err := os.ReadFile(filepath.Join(dir, isoNames[0]))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "already-here" {
		t.Fatalf("pre-existing ISO content must be preserved untouched, got %q", body)
	}
}
