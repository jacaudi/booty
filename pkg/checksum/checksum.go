// Package checksum parses `sha256sum`-format digest files.
//
// netboot.xyz's per-release sha256-checksums.txt and Debian's cdimage
// SHA256SUMS are byte-identical in shape ("<hexdigest><space><space><filename>",
// one entry per line, no "./" prefix and no binary-mode "*"), so the format is
// ONE piece of knowledge with one implementation.
//
// It is a stdlib-only leaf package rather than a helper in pkg/cache or
// pkg/config: pkg/cache imports pkg/ostype, so pkg/cache cannot host something
// pkg/ostype needs, and a file-format parser is not configuration.
package checksum

import (
	"fmt"
	"strings"
)

// ParseSums parses `sha256sum` binary-mode output into filename -> hexdigest.
// Blank lines are skipped; any other line lacking the two-space separator is an
// error. Failing loud matters: a partially-parsed digest file would silently
// leave files unverified, which is the silent downgrade the caller's fail-loud
// policy exists to forbid.
//
// An empty body yields an empty map and no error — whether "no entry for the
// file I care about" is acceptable is the caller's decision, not the parser's.
func ParseSums(body []byte) (map[string]string, error) {
	sums := make(map[string]string)
	for line := range strings.Lines(string(body)) {
		line = strings.TrimRight(line, "\n")
		if line == "" {
			continue
		}
		digest, name, ok := strings.Cut(line, "  ")
		if !ok {
			return nil, fmt.Errorf("checksum: malformed line %q", line)
		}
		sums[name] = digest
	}
	return sums, nil
}
