package cache

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// SweepPartials removes stray *.partial files left by a crash mid-download,
// anywhere under root. Called at the top of every reconcile pass so a killed
// download self-heals; a normal pass's own in-flight partials are created after
// this runs and renamed/deleted before the pass ends.
func SweepPartials(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort: skip unreadable subtrees
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".partial") {
			if rerr := os.Remove(p); rerr != nil {
				slog.Warn("cache: sweep partial failed", "path", p, "err", rerr)
			}
		}
		return nil
	})
}
