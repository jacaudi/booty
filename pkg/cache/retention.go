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
	// Clamp before any truncation. Both branches below slice with [:n], which
	// on a negative n is a bounds panic on the reconcile goroutine — i.e. the
	// whole process. The API paths validate RetainN >= 0, but this runs on
	// whatever is already in the targets table (a row predating that gate, a
	// direct DB edit), so the arithmetic must be safe on its own. "Retain -5"
	// has no meaning beyond "retain nothing", which is what retain: 0 means.
	if n < 0 {
		n = 0
	}
	if canonicalOS == "talos" {
		return selectRetained(versions, n)
	}
	o, ok := ostype.Lookup(canonicalOS)
	if !ok {
		return []string{}
	}
	// Tool OSes: netboot.xyz publishes exactly ONE release per endpoint, and
	// Artifacts refuses any version that is not that current tag — so a
	// non-current tag can never be re-landed anyway. Retain the discovered set
	// verbatim rather than sorting it, then cap it at n (same shape as the
	// non-tool branch below).
	//
	// Sorting would be actively WRONG here: tool tags have no version grammar
	// (CompareVersions is a string compare), so a newer tag that sorts lexically
	// lower ("10.00-…" < "9.05-…") would lose to the cached one and the target
	// would pin the old release forever.
	//
	// Truncating to the first n elements (rather than re-sorting to pick which
	// n to keep) is only correct because the caller — reconcileTarget's `known`
	// construction in reconcile.go — always places the freshly-discovered tag(s)
	// first: `known := slices.Clone(discovered)`, then any in-window-cached
	// version NOT already in discovered is appended after. That ordering is a
	// non-local invariant this function relies on but cannot enforce: a future
	// change to reconcile.go that reorders or sorts `known` before calling
	// retentionFor would silently reintroduce the "keep the old tag forever"
	// bug this branch exists to fix, and no test here would catch it — the
	// tool retention test hand-constructs its input already discovered-first.
	if o.Family().Name == "tool" {
		out := slices.Clone(versions)
		if n < len(out) {
			out = out[:n]
		}
		return out
	}
	sorted := slices.Clone(versions)
	slices.SortFunc(sorted, func(a, b string) int { return o.CompareVersions(b, a) }) // newest first
	if n < len(sorted) {
		sorted = sorted[:n]
	}
	return sorted
}
