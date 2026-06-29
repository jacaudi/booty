package cache

import (
	"os"
	"path/filepath"

	"github.com/jeefy/booty/pkg/ostype"
)

// NewestCached returns the newest valid version directory currently present
// under cache/<cacheName>/<segment>/<arch>/, or "" if none. cacheName is the
// ON-DISK name (e.g. "coreos"), as the boot path uses it; ordering and version
// validation use the corresponding ostype (e.g. fedora-coreos) so each OS's
// own CompareVersions decides "newest". This generalizes the disk scan that
// pkg/versions.NewestCachedTalos did for Talos only. Returning "" reproduces
// the pre-first-sync 404 failure mode (boot BASEURL points at a missing dir).
func NewestCached(cacheName, arch string, params map[string]string) string {
	o, ok := ostype.Lookup(cacheNameToCanonical(cacheName))
	if !ok {
		return ""
	}
	base := filepath.Join(cacheRoot(), cacheName, paramSegment(params), arch)
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	newest := ""
	for _, e := range entries {
		if !e.IsDir() || o.ValidateVersion(e.Name()) != nil {
			continue
		}
		if newest == "" || o.CompareVersions(e.Name(), newest) > 0 {
			newest = e.Name()
		}
	}
	return newest
}
