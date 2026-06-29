package cache

import (
	"slices"

	"github.com/jeefy/booty/pkg/ostype"
	"golang.org/x/mod/semver"
)

// selectRetained returns the highest-patch tag for each of the newest n minor
// lines (e.g. v1.10.x, v1.9.x, v1.8.x), newest line first. Invalid tags and
// prereleases are dropped — the factory list is an untrusted boundary, so this
// uses semver validation rather than a regex. Pure function; table-tested.
// (Relocated verbatim from pkg/versions/talos.go.)
func selectRetained(tags []string, n int) []string {
	best := map[string]string{} // MajorMinor -> highest patch tag
	for _, tag := range tags {
		if !semver.IsValid(tag) || semver.Prerelease(tag) != "" {
			continue
		}
		mm := semver.MajorMinor(tag)
		if cur, ok := best[mm]; !ok || semver.Compare(tag, cur) > 0 {
			best[mm] = tag
		}
	}
	lines := make([]string, 0, len(best))
	for mm := range best {
		lines = append(lines, mm)
	}
	slices.SortFunc(lines, func(a, b string) int { return semver.Compare(b, a) })

	out := []string{}
	for i, mm := range lines {
		if i >= n {
			break
		}
		out = append(out, best[mm])
	}
	return out
}

// retentionFor selects which discovered versions to keep for one target.
//
// ponytail: Talos is the only OS that retains by MINOR line; every other OS
// (single-version discovery for Flatcar/CoreOS, point releases for Debian)
// keeps the newest n by CompareVersions. This is a Talos-keyed branch, not an
// OS-interface method, because there is exactly one grouping OS today — promote
// it to an ostype.OS method when a 2nd grouping OS appears (YAGNI/No-Wall: do
// not widen the frozen P1a interface for a variant that does not exist).
func retentionFor(canonicalOS string, versions []string, n int) []string {
	if canonicalOS == "talos" {
		return selectRetained(versions, n)
	}
	o, ok := ostype.Lookup(canonicalOS)
	if !ok {
		return []string{}
	}
	sorted := slices.Clone(versions)
	slices.SortFunc(sorted, func(a, b string) int { return o.CompareVersions(b, a) }) // newest first
	if n < len(sorted) {
		sorted = sorted[:n]
	}
	return sorted
}
