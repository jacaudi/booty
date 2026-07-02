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
	"regexp"

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

// artifactPath returns the on-disk path an artifact URL resolves to inside dir,
// using the SAME trailing-path-segment derivation ensureArtifact/DownloadFile
// use. It is the single source for "where did this URL's bytes land".
func artifactPath(dir, srcURL string) (string, error) {
	u, err := url.Parse(srcURL)
	if err != nil {
		return "", fmt.Errorf("cache: parse url %q: %w", srcURL, err)
	}
	return filepath.Join(dir, path.Base(u.Path)), nil
}

// ensureArtifact downloads srcURL into dir if not already present (idempotent).
// The on-disk filename is the URL's trailing path segment (query stripped) —
// the SAME derivation config.DownloadFile uses — so the existence check and the
// written file always agree.
func ensureArtifact(ctx context.Context, dir, srcURL string) error {
	dst, err := artifactPath(dir, srcURL)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		slog.Debug("artifact already cached", "file", dst)
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
// cache segment: schematic (talos) > channel (flatcar/fcos/debian) > "-".
// No OS carries both keys, so the precedence order is theoretical; documented
// anyway. (Layout invariant, design §2.3: exactly one discriminating segment.)
func paramSegment(params map[string]string) string {
	if s := params["schematic"]; s != "" {
		return s
	}
	if c := params["channel"]; c != "" {
		return c
	}
	return "-"
}

// pathParamRE admits single-segment path-safe values: lowercase alnum start,
// then alnum/dot/dash/underscore. No "/", no leading dot — so a value can
// never traverse out of its cache segment ("a..b" is an odd but harmless
// single segment; a literal ".." or "" is rejected).
var pathParamRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidatePathParam rejects a value that cannot safely become a cache path
// segment (disk dir + URL). Single knowledge site for "values that become
// path segments must be path-safe": it guards ALL such values — params
// (schematic/channel) AND arch — and is called by the API create handler,
// seedTargets, and the startup migration.
func ValidatePathParam(v string) error {
	if !pathParamRE.MatchString(v) {
		return fmt.Errorf("cache: value %q is not path-safe (must match %s)", v, pathParamRE)
	}
	return nil
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

// EncodeParams is the canonical params encoder shared with the API layer so
// targets created via /api/v1 collide on UNIQUE(os,arch,params) exactly as
// reconciler-seeded ones do. See encodeParams.
func EncodeParams(params map[string]string) (string, error) { return encodeParams(params) }

// DecodeParams parses a canonical params string. See decodeParams.
func DecodeParams(s string) (map[string]string, error) { return decodeParams(s) }
