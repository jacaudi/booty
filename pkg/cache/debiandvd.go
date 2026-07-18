// Package cache: Debian-DVD-specific verification. Later tasks add more to
// this file (ISO9660 extraction, pool merge, reconciler branch).
package cache

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/diskfs/go-diskfs"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/ostype"
)

// debianCDKeyring is the armored Debian CD signing public key used to verify
// a Debian DVD set's SHA256SUMS. It is a package-var seam (not a direct call
// to ostype.DebianCDKeyring()) so tests can inject a fixture keyring via
// swapDebianKeyring without touching the embedded asset.
var debianCDKeyring = ostype.DebianCDKeyring()

// verifyDVDChecksums GPG-verifies dir/SHA256SUMS against dir/SHA256SUMS.sign
// (offline, via verifyDetachedGPGLocal) against the Debian CD signing key,
// then checksums each isoNames[i] in dir against the verified sums. Returns a
// non-nil error on any GPG failure, a missing sums entry, or a checksum
// mismatch.
func verifyDVDChecksums(ctx context.Context, dir string, isoNames []string) error {
	sumsPath := filepath.Join(dir, "SHA256SUMS")
	if err := verifyDetachedGPGLocal(sumsPath, sumsPath+".sign", debianCDKeyring); err != nil {
		return fmt.Errorf("cache: GPG-verify SHA256SUMS: %w", err)
	}
	sums, err := parseSHA256SUMS(sumsPath)
	if err != nil {
		return err
	}
	for _, name := range isoNames {
		want, ok := sums[name]
		if !ok {
			return fmt.Errorf("cache: %s not listed in SHA256SUMS", name)
		}
		// hashFile (verify.go) is the single source for "how we sha256 a file
		// for verification" — reused here rather than a second implementation.
		got, err := hashFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("cache: checksum mismatch for %s: got %s want %s", name, got, want)
		}
	}
	return nil
}

// dvdSentinelName marks a final DVD tree as fully merged (design I7). Its
// presence — not mtimes or mode bits — is the idempotency signal: a settled
// tree is never re-extracted.
const dvdSentinelName = ".booty-dvd-complete"

// dvdSentinelPresent reports whether dir's DVD merge has already completed.
func dvdSentinelPresent(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, dvdSentinelName))
	return err == nil
}

// isoExtractFunc extracts one ISO9660 image's full contents into destDir,
// preserving relative paths (creating destDir as needed). isoExtractor is a
// package-var seam — like debianCDKeyring above — so the merge-logic test can
// inject a fake that lays down fixture trees without touching a real ISO.
type isoExtractFunc func(ctx context.Context, isoPath, destDir string) error

var isoExtractor isoExtractFunc = extractISO

// extractISO opens isoPath as an ISO9660 (Rock Ridge) filesystem via
// diskfs/go-diskfs and streams every regular file into destDir at its
// relative path. Adoption of diskfs is PROVISIONAL: task-6-brief.md's Step-0
// real-ISO validation spike (isospike_integration_test.go) is deferred to the
// netboot lab and has not run in this session.
func extractISO(ctx context.Context, isoPath, destDir string) error {
	d, err := diskfs.Open(isoPath, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		return fmt.Errorf("cache: open ISO %s: %w", isoPath, err)
	}
	defer d.Close()
	fsys, err := d.GetFilesystem(0)
	if err != nil {
		return fmt.Errorf("cache: read ISO9660 filesystem in %s: %w", isoPath, err)
	}
	if err := copyFSTree(ctx, fsys, destDir, true); err != nil {
		return fmt.Errorf("cache: extract %s: %w", isoPath, err)
	}
	return nil
}

// copyTree walks src (a real on-disk directory, e.g. one disc's staged
// extraction) and streams every regular file to the same relative path under
// dst, creating parent directories as needed. When overwrite is false, a
// destination file that already exists is left untouched — this is how
// unioning pool/ across discs keeps whichever disc was processed first
// (disc-1) authoritative on any name collision (design §6.3).
func copyTree(ctx context.Context, src, dst string, overwrite bool) error {
	if err := copyFSTree(ctx, os.DirFS(src), dst, overwrite); err != nil {
		return fmt.Errorf("cache: copy %s: %w", src, err)
	}
	return nil
}

// copyFSTree walks every regular file in srcFS and streams it to the same
// relative path under dst, creating parent directories as needed. It is the
// single walker shared by extractISO (srcFS = an ISO9660 image opened via
// diskfs) and copyTree (srcFS = os.DirFS of a real staged directory) — both
// are "stream every file from a readable tree to an OS destination tree",
// differing only in what implements fs.FS. When overwrite is false, a
// destination file that already exists is left untouched.
func copyFSTree(ctx context.Context, srcFS fs.FS, dst string, overwrite bool) error {
	return fs.WalkDir(srcFS, ".", func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", p, walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		target := filepath.Join(dst, filepath.FromSlash(p))
		if !overwrite {
			if _, err := os.Stat(target); err == nil {
				return nil // already present from an earlier disc; keep it
			}
		}
		src, err := srcFS.Open(p)
		if err != nil {
			return fmt.Errorf("open %s: %w", p, err)
		}
		defer src.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", target, err)
		}
		out, err := os.Create(target)
		if err != nil {
			return fmt.Errorf("create %s: %w", target, err)
		}
		defer out.Close()
		if _, err := io.Copy(out, src); err != nil {
			return fmt.Errorf("copy %s to %s: %w", p, target, err)
		}
		return nil
	})
}

// extractAndMergeISO extracts each of isoNames (verbatim ISOs already
// downloaded to isoDir and GPG+checksum verified by verifyDVDChecksums) via
// the isoExtractor seam into a disposable staging tree, merges per design
// §6.3 — disc-1's dists/ and install.<arch>/ served verbatim; every disc's
// pool/ unioned, disc-1 winning any name collision — then moves the three
// merged subtrees into final by per-subtree os.Rename and writes the
// completion sentinel LAST. final is never removed wholesale: it already
// holds the retained verbatim ISOs (design §6.1); only the three named
// subtrees within it are replaced, which also makes a retry after a partial
// failure safe (design I7).
func extractAndMergeISO(ctx context.Context, isoDir string, isoNames []string, final, arch string) error {
	staging := final + ".tree-staging"
	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("cache: clean stale staging %s: %w", staging, err)
	}
	defer os.RemoveAll(staging)

	merged := filepath.Join(staging, "merged")
	installDir := "install." + arch

	for i, name := range isoNames {
		if err := ctx.Err(); err != nil {
			return err
		}
		discDir := filepath.Join(staging, fmt.Sprintf("disc-%d", i+1))
		if err := isoExtractor(ctx, filepath.Join(isoDir, name), discDir); err != nil {
			return fmt.Errorf("cache: extract %s: %w", name, err)
		}
		if i == 0 {
			if err := copyTree(ctx, filepath.Join(discDir, "dists"), filepath.Join(merged, "dists"), true); err != nil {
				return fmt.Errorf("cache: merge dists from %s: %w", name, err)
			}
			if err := copyTree(ctx, filepath.Join(discDir, installDir), filepath.Join(merged, installDir), true); err != nil {
				return fmt.Errorf("cache: merge %s from %s: %w", installDir, name, err)
			}
		}
		if err := copyTree(ctx, filepath.Join(discDir, "pool"), filepath.Join(merged, "pool"), false); err != nil {
			return fmt.Errorf("cache: union pool from %s: %w", name, err)
		}
	}

	if err := os.MkdirAll(final, 0o755); err != nil {
		return fmt.Errorf("cache: mkdir %s: %w", final, err)
	}
	for _, subtree := range []string{"dists", "pool", installDir} {
		dst := filepath.Join(final, subtree)
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("cache: clear stale %s: %w", dst, err)
		}
		if err := os.Rename(filepath.Join(merged, subtree), dst); err != nil {
			return fmt.Errorf("cache: move %s into final: %w", subtree, err)
		}
	}

	if err := os.WriteFile(filepath.Join(final, dvdSentinelName), nil, 0o644); err != nil {
		return fmt.Errorf("cache: write sentinel: %w", err)
	}
	return nil
}

// isoDownload/isoVerify/isoExtract are package-var seams over the heavy Debian
// DVD ops (download/verify/extract-and-merge) — the same strategy as
// debianCDKeyring and isoExtractor above — so ensureDebianDVD's state machine
// (this file, below) can be tested without real network or real ISOs.
var (
	isoDownload = downloadLargeFile
	isoVerify   = verifyDVDChecksums
	isoExtract  = extractAndMergeISO
)

// wantsDVD reports whether a target's effective or pending serving mode is
// DVD — the reconciler dispatch trigger (reconcile.go) and ensureDebianDVD's
// mode-flip guard both consult it.
func wantsDVD(t db.Target) bool {
	return t.SourceMode == "dvd" || t.DesiredMode == "dvd"
}

// debianDVDVersion resolves the newest point release for a DVD-wanted Debian
// target via o.DiscoverVersions (which returns the single newest release,
// newest first — ostype/debian.go), erroring on an empty or failed result so
// the caller can log and retry next tick instead of caching an empty version.
func debianDVDVersion(ctx context.Context, o ostype.OS, params map[string]string) (string, error) {
	versions, err := o.DiscoverVersions(ctx, params)
	if err != nil {
		return "", err
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("cache: debian dvd: no version discovered")
	}
	return versions[0], nil
}

// debianDVDSources builds the cdimage.debian.org URLs for one DVD set (design
// §5): the current stable suite (segment=="13") is served live from
// debian-cd/current/; older suites are served from their point-release
// archive dir. ISO names follow debian-<version>-<arch>-DVD-<n>.iso for
// n = 1..count; SHA256SUMS and its detached signature live alongside them.
func debianDVDSources(segment, arch, version string, count int) (isoNames, isoURLs []string, sumsURL, sigURL string) {
	base := "https://cdimage.debian.org/cdimage/archive/" + version + "/" + arch + "/iso-dvd/"
	if segment == "13" {
		base = "https://cdimage.debian.org/debian-cd/current/" + arch + "/iso-dvd/"
	}
	isoNames = make([]string, count)
	isoURLs = make([]string, count)
	for i := range count {
		isoNames[i] = fmt.Sprintf("debian-%s-%s-DVD-%d.iso", version, arch, i+1)
		isoURLs[i] = base + isoNames[i]
	}
	return isoNames, isoURLs, base + "SHA256SUMS", base + "SHA256SUMS.sign"
}

// dirSize walks dir summing the size of every regular file — the size
// accounting the generic land path (reconcile.go) would otherwise record via
// per-artifact os.Stat.
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, ierr := entry.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// ensureDebianDVD brings target t to source_mode=dvd for version: downloading
// (isoDownload), GPG+checksum verifying (isoVerify), and extracting+merging
// (isoExtract) the DVD set, then recording the DB rows the generic reconcile
// path would have made (manual source, never archived; pinned, never
// evicted — §11.2) and flipping source_mode LAST.
//
// Only the HEAVY work (download/verify/extract) is gated on the extraction
// sentinel (dvdSentinelPresent) — that is the idempotency signal that a
// settled tree is never re-downloaded. The accounting+pin+flip that follows
// runs on BOTH paths (fresh extract OR sentinel already present) and is fully
// idempotent (upserts/updates keyed on stable identities), so it SELF-HEALS a
// prior run that wrote the sentinel but died (or hit a transient SQLITE_BUSY)
// before the DB mutations landed — otherwise a sentinel-present short-circuit
// would leave a dvd-serving target with no cache_entries row forever (invisible
// in the UI, uncounted in SumCacheBytes, no never-evict pin).
//
// A failed/partial DOWNLOAD returns before the sentinel is written and before
// any DB mutation, leaving source_mode=netinst + desired_mode=dvd for the next
// tick to retry from scratch.
func ensureDebianDVD(ctx context.Context, store *db.Store, t db.Target, version string) error {
	params, err := decodeParams(t.Params)
	if err != nil {
		return err
	}
	cacheName := canonicalToCacheName(t.OS) // "debian"
	segment := paramSegment(params)         // channel, e.g. "12"
	dir := cacheDir(cacheName, segment, t.Arch, version)

	if !dvdSentinelPresent(dir) { // heavy work only when the tree is not yet settled
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		isoNames, isoURLs, sumsURL, sigURL := debianDVDSources(segment, t.Arch, version, t.DvdCount)
		for i := range isoURLs {
			if err := isoDownload(ctx, isoURLs[i], filepath.Join(dir, isoNames[i])); err != nil {
				return fmt.Errorf("cache: download %s: %w", isoNames[i], err)
			}
		}
		if err := isoDownload(ctx, sumsURL, filepath.Join(dir, "SHA256SUMS")); err != nil {
			return err
		}
		if err := isoDownload(ctx, sigURL, filepath.Join(dir, "SHA256SUMS.sign")); err != nil {
			return err
		}
		if err := isoVerify(ctx, dir, isoNames); err != nil {
			return err
		}
		if err := isoExtract(ctx, dir, isoNames, dir, t.Arch); err != nil { // writes sentinel LAST
			return err
		}
	}

	// Sentinel now guaranteed present (pre-existing or just written). Idempotently
	// record the DB rows the generic path would have made: manual source (never
	// archived) + pinned (never evicted, §11.2). size = walked bytes of the dir.
	if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: t.ID, Version: version, Source: "manual", Cached: true}); err != nil {
		return err
	}
	tvID, err := store.TargetVersionID(t.ID, version)
	if err != nil {
		return err
	}
	if err := store.UpsertCacheEntry(tvID, dirSize(dir)); err != nil {
		return err
	}
	if err := store.SetCachePinnedByTargetVersion(tvID, true); err != nil {
		return err
	}
	if err := store.SetTargetSourceMode(t.ID, "dvd"); err != nil { // flip LAST
		return err
	}
	// Best-effort: reconcile away any prior netinst-cached versions for this
	// target — superseded by the DVD tree. Remove the dir AND delete the DB row
	// (cache_entries cascade-deletes) so disk and DB stay consistent; a kept
	// cached=1/size>0 row pointing at a removed dir would permanently overcount
	// SumCacheBytes and surface a phantom version in the UI. The DVD version row
	// (manual, pinned) is kept.
	if existing, lerr := store.ListTargetVersions(t.ID); lerr == nil {
		for _, v := range existing {
			if v.Version != version {
				_ = removeVersionDir(cacheName, segment, t.Arch, v.Version)
				_ = store.DeleteTargetVersion(t.ID, v.Version)
			}
		}
	}
	return nil
}

// parseSHA256SUMS parses a `sha256sum`-format file (binary mode: "hexdigest
// <space><space>filename") into a map[filename]hexdigest.
func parseSHA256SUMS(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cache: read %s: %w", path, err)
	}
	sums := make(map[string]string)
	for line := range strings.Lines(string(body)) {
		line = strings.TrimRight(line, "\n")
		if line == "" {
			continue
		}
		digest, name, ok := strings.Cut(line, "  ")
		if !ok {
			return nil, fmt.Errorf("cache: %s: malformed line %q", path, line)
		}
		sums[name] = digest
	}
	return sums, nil
}
