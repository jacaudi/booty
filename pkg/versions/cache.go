package versions

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

// cacheRoot returns <dataDir>/cache.
func cacheRoot() string {
	return filepath.Join(viper.GetString(config.DataDir), "cache")
}

// cacheSegments is the ONE source of truth for the cache layout ordering:
// <os>/<schematic-or-dash>/<arch>/<version>. schematic is "-" for OSes
// without a schematic concept (Flatcar, CoreOS). Both cacheDir and
// cacheURLBase build on this so the disk path and the client URL cannot
// silently diverge and produce boot 404s.
func cacheSegments(osName, schematic, arch, version string) []string {
	return []string{osName, schematic, arch, version}
}

// cacheDir returns the absolute version-scoped directory for an artifact set.
func cacheDir(osName, schematic, arch, version string) string {
	return filepath.Join(append([]string{cacheRoot()}, cacheSegments(osName, schematic, arch, version)...)...)
}

// cacheURLBase returns the client-facing base URL for the same directory,
// e.g. <server>/data/cache/<os>/<schematic>/<arch>/<version>.
func cacheURLBase(server, osName, schematic, arch, version string) string {
	return server + "/data/cache/" + path.Join(cacheSegments(osName, schematic, arch, version)...)
}

// ensureArtifact downloads the artifact at srcURL into dir if it is not already
// present (idempotent). The on-disk filename is the URL's trailing path segment
// (query stripped) — the SAME derivation config.DownloadFile uses — so the
// existence check and the written file always agree. MkdirAll runs before the
// download so os.Create never fails on a missing parent.
func ensureArtifact(ctx context.Context, dir, srcURL string) error {
	u, err := url.Parse(srcURL)
	if err != nil {
		return fmt.Errorf("versions: parse url %q: %w", srcURL, err)
	}
	filename := path.Base(u.Path)
	if _, err := os.Stat(filepath.Join(dir, filename)); err == nil {
		slog.Debug("artifact already cached", "file", filepath.Join(dir, filename))
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return config.DownloadFile(ctx, dir, srcURL)
}

// removeVersionDir removes a single version-scoped directory and its contents.
// A missing directory is not an error (first-run / already-clean).
func removeVersionDir(osName, schematic, arch, version string) error {
	return os.RemoveAll(cacheDir(osName, schematic, arch, version))
}
