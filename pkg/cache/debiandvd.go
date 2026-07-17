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
