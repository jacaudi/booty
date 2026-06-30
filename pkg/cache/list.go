// pkg/cache/list.go
package cache

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeefy/booty/pkg/ostype"
)

// CacheEntry is one bootable artifact set present on disk. The names are the
// ON-DISK cache names/segments (e.g. "coreos"), matching the boot path — NOT the
// taxonomy canonical names.
type CacheEntry struct {
	CacheName string // on-disk <os> segment: flatcar | coreos | talos
	Segment   string // schematic, or "-"
	Arch      string
	Version   string
}

// ListCached walks cache/<cacheName>/<segment>/<arch>/<version> and returns every
// version directory whose name passes the corresponding ostype's ValidateVersion
// — the SAME filter NewestCached applies. Entries are sorted CacheName asc, then
// Segment asc, then version DESC (newest first) for a stable, friendly menu order.
// Unknown cache names (no ostype) and invalid version dirs are skipped. It is the
// multi-version generalization of NewestCached; the two stay separate by design.
func ListCached() []CacheEntry {
	root := cacheRoot()
	var out []CacheEntry
	cacheNames, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, cn := range cacheNames {
		if !cn.IsDir() {
			continue
		}
		o, ok := ostype.Lookup(cacheNameToCanonical(cn.Name()))
		if !ok {
			continue
		}
		segs, err := os.ReadDir(filepath.Join(root, cn.Name()))
		if err != nil {
			continue
		}
		for _, seg := range segs {
			if !seg.IsDir() {
				continue
			}
			arches, err := os.ReadDir(filepath.Join(root, cn.Name(), seg.Name()))
			if err != nil {
				continue
			}
			for _, a := range arches {
				if !a.IsDir() {
					continue
				}
				vers, err := os.ReadDir(filepath.Join(root, cn.Name(), seg.Name(), a.Name()))
				if err != nil {
					continue
				}
				for _, v := range vers {
					if !v.IsDir() || o.ValidateVersion(v.Name()) != nil {
						continue
					}
					out = append(out, CacheEntry{
							CacheName: cn.Name(),
							Segment:   seg.Name(),
							Arch:      a.Name(),
							Version:   v.Name(),
						})
				}
			}
		}
	}
	slices.SortStableFunc(out, func(a, b CacheEntry) int {
		if c := cmp.Compare(a.CacheName, b.CacheName); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Segment, b.Segment); c != 0 {
			return c
		}
		o, _ := ostype.Lookup(cacheNameToCanonical(a.CacheName)) // ok guaranteed: walk filter above accepted this cacheName
		return -o.CompareVersions(a.Version, b.Version) // newest first
	})
	return out
}

// CacheDirExists reports whether the version-scoped artifact directory exists.
func CacheDirExists(cacheName, segment, arch, version string) bool {
	info, err := os.Stat(cacheDir(cacheName, segment, arch, version))
	return err == nil && info.IsDir()
}

// containsTraversal reports whether s contains a path-traversal sequence ("..") or
// a path separator ("/"). Used to reject segment and arch values that would escape
// the cache subtree when joined into a filesystem path.
func containsTraversal(s string) bool {
	return strings.Contains(s, "..") || strings.Contains(s, "/")
}

// ValidCachedSelection reports whether (cacheName, segment, arch, version) names
// a real, bootable cache entry: cacheName maps to a known OS, the version passes
// that OS's ValidateVersion, and the directory exists on disk. The menu
// selection-boot path uses this to reject malformed/unknown/traversal selections
// before rendering — arbitrary disk content is never served.
func ValidCachedSelection(cacheName, segment, arch, version string) bool {
	// Reject traversal in the two unsanitized fields before constructing any path.
	// cacheName is bounded by ostype.Lookup below; version is bounded by ValidateVersion.
	if containsTraversal(segment) || containsTraversal(arch) {
		return false
	}
	o, ok := ostype.Lookup(cacheNameToCanonical(cacheName))
	if !ok {
		return false
	}
	if o.ValidateVersion(version) != nil {
		return false
	}
	return CacheDirExists(cacheName, segment, arch, version)
}
