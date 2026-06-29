// Package cache owns booty's on-disk artifact cache: the single source of the
// directory + URL layout, and the reconciler that eagerly fills and prunes it
// from operator-declared targets (replacing the per-OS version crons).
package cache

import (
	"context"
	"encoding/json"
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
// <os>/<schematic-or-dash>/<arch>/<version>. schematic is "-" for OSes without
// a schematic concept (Flatcar, CoreOS). Both cacheDir (disk) and CacheURLBase
// (client URL) build on this so they cannot silently diverge and 404 boots.
func cacheSegments(osName, schematic, arch, version string) []string {
	return []string{osName, schematic, arch, version}
}

// cacheDir returns the absolute version-scoped directory for an artifact set.
func cacheDir(osName, schematic, arch, version string) string {
	return filepath.Join(append([]string{cacheRoot()}, cacheSegments(osName, schematic, arch, version)...)...)
}

// CacheURLBase returns the client-facing base URL for the same directory:
// <server>/data/cache/<os>/<schematic>/<arch>/<version>.
func CacheURLBase(server, osName, schematic, arch, version string) string {
	return server + "/data/cache/" + path.Join(cacheSegments(osName, schematic, arch, version)...)
}

// ensureArtifact downloads srcURL into dir if not already present (idempotent).
// The on-disk filename is the URL's trailing path segment (query stripped) —
// the SAME derivation config.DownloadFile uses — so the existence check and the
// written file always agree.
func ensureArtifact(ctx context.Context, dir, srcURL string) error {
	u, err := url.Parse(srcURL)
	if err != nil {
		return fmt.Errorf("cache: parse url %q: %w", srcURL, err)
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
// A missing directory is not an error (already-clean / first run).
func removeVersionDir(osName, schematic, arch, version string) error {
	return os.RemoveAll(cacheDir(osName, schematic, arch, version))
}

// cacheNameByCanonical is the SINGLE source of the on-disk-name bridge: the
// taxonomy's canonical OS name (left) maps to the irreversible on-disk cache
// segment (right). Only fedora-coreos differs (disk stays "coreos"); every
// other OS is identity. The two accessors below are the only callers.
var cacheNameByCanonical = map[string]string{"fedora-coreos": "coreos"}

// canonicalToCacheName maps a taxonomy canonical name to its on-disk segment.
func canonicalToCacheName(canonical string) string {
	if n, ok := cacheNameByCanonical[canonical]; ok {
		return n
	}
	return canonical
}

// cacheNameToCanonical maps an on-disk segment back to the canonical name so the
// boot path (which speaks cache names) can pick the ostype for version ordering.
func cacheNameToCanonical(name string) string {
	for canon, cn := range cacheNameByCanonical {
		if cn == name {
			return canon
		}
	}
	return name
}

// paramSegment encodes a target's params into the single path-discriminating
// cache segment: the Talos schematic when present, else "-" (Flatcar/CoreOS).
// (Layout invariant, design §2.3: exactly one path-discriminating segment.)
func paramSegment(params map[string]string) string {
	if s := params["schematic"]; s != "" {
		return s
	}
	return "-"
}

// encodeParams is the one canonical params encoder: json.Marshal emits map keys
// sorted, so equal param sets always produce equal strings — the invariant the
// targets UNIQUE(os,arch,params) constraint and db.Target.Params depend on. nil
// or empty encodes to "{}".
func encodeParams(params map[string]string) (string, error) {
	if len(params) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("cache: encode params: %w", err)
	}
	return string(b), nil
}

// decodeParams parses a canonical params string back into a map. "" and "{}"
// both decode to an empty map.
func decodeParams(s string) (map[string]string, error) {
	if s == "" || s == "{}" {
		return map[string]string{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("cache: decode params %q: %w", s, err)
	}
	return m, nil
}
