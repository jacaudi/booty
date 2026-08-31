package http

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

// errPathEscapesDataDir reports a name that resolves outside dataDir. Callers
// map it to 400/500 and MUST NOT echo the resolved path back to the client.
var errPathEscapesDataDir = errors.New("http: path escapes dataDir")

// resolveWithinDataDir resolves name against dataDir and returns an absolute
// path guaranteed to lie inside it, or errPathEscapesDataDir.
//
// It is stricter than pkg/tftp's safeJoin (tftp.go:64) in one way that matters
// here: it calls EvalSymlinks and re-checks containment, because the consumers
// are template.ParseFiles and os.ReadFile, both of which FOLLOW symlinks. A
// link planted inside dataDir pointing out would otherwise be served.
//
// The containment check is separator-terminated on purpose: a plain
// strings.HasPrefix(resolved, dataDir) would accept a sibling directory named
// "<dataDir>-evil", which shares the prefix but is not inside.
func resolveWithinDataDir(name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("%w: %q", errPathEscapesDataDir, name)
	}
	root, err := filepath.Abs(viper.GetString(config.DataDir))
	if err != nil {
		return "", fmt.Errorf("http: resolve dataDir: %w", err)
	}
	// Resolve the ROOT's own symlinks first, so a symlinked dataDir (common on
	// macOS, where /tmp is a link to /private/tmp) does not make every
	// legitimate name look like an escape.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	joined := filepath.Clean(filepath.Join(root, name))
	if !within(root, joined) {
		return "", fmt.Errorf("%w: %q", errPathEscapesDataDir, name)
	}

	// EvalSymlinks also stats, so a missing file surfaces here as
	// os.ErrNotExist -- deliberately distinct from an escape, because the two
	// map to different HTTP statuses.
	final, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("http: resolve %q under dataDir: %w", name, err)
	}
	if !within(root, final) {
		return "", fmt.Errorf("%w: %q resolves outside dataDir", errPathEscapesDataDir, name)
	}
	return final, nil
}

// within reports whether p is strictly inside root. Separator-terminated, so
// "<root>-evil" does not satisfy it.
func within(root, p string) bool {
	return strings.HasPrefix(p, root+string(os.PathSeparator))
}
