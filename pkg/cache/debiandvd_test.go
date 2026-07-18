package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
