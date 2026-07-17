// Package cache: Debian-DVD-specific verification. Later tasks add more to
// this file (ISO9660 extraction, pool merge, reconciler branch).
package cache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
