package ostype

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/jeefy/booty/pkg/checksum"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v4"
)

// netbootxyzEntry is one endpoints.yml entry. Every field is a string or a
// []string on purpose: decoding through any/map[string]any turns an unquoted
// 13.01 into a float64, and a future 7.10 would render as "7.1".
//
// Version is decode-only — booty's on-disk version is the release tag from Path,
// not this field (see releaseTag). It is kept because omitting it from the
// struct would make the decode silently lossy for anyone debugging the manifest.
type netbootxyzEntry struct {
	Path    string   `yaml:"path"`
	Files   []string `yaml:"files"`
	Version string   `yaml:"version"`
}

// netbootxyzDoc is the top-level manifest. Unknown keys are tolerated
// deliberately: entries carry os/arch/flavor/kernel and upstream adds more
// freely, so yaml.WithKnownFields would break on the next upstream addition.
type netbootxyzDoc struct {
	Endpoints map[string]netbootxyzEntry `yaml:"endpoints"`
}

// netbootxyzCache memoizes the endpoints manifest. Same shape as streamsCache:
// guarded by a mutex because VerifyVersion -> Artifacts runs on the API
// goroutine, and reset explicitly rather than on a timer. No single-flight —
// reconcileAll iterates targets sequentially on one goroutine.
var netbootxyzCache = struct {
	sync.Mutex
	endpoints map[string]netbootxyzEntry
}{}

// fetchNetbootxyzDoc returns the endpoint manifest, fetching it at most once
// between ResetNetbootxyzCache calls.
func fetchNetbootxyzDoc(ctx context.Context) (map[string]netbootxyzEntry, error) {
	netbootxyzCache.Lock()
	if netbootxyzCache.endpoints != nil {
		eps := netbootxyzCache.endpoints
		netbootxyzCache.Unlock()
		return eps, nil
	}
	netbootxyzCache.Unlock()

	url := viper.GetString(config.NetbootxyzEndpointsURL)
	if url == "" {
		return nil, fmt.Errorf("ostype: netbootxyz: %s is unset", config.NetbootxyzEndpointsURL)
	}
	body, err := fetchMetadata(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("ostype: netbootxyz: fetch endpoints: %w", err)
	}
	var doc netbootxyzDoc
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("ostype: netbootxyz: parse endpoints: %w", err)
	}
	if len(doc.Endpoints) == 0 {
		return nil, fmt.Errorf("ostype: netbootxyz: endpoints manifest is empty")
	}

	netbootxyzCache.Lock()
	netbootxyzCache.endpoints = doc.Endpoints
	netbootxyzCache.Unlock()
	return doc.Endpoints, nil
}

// ResetNetbootxyzCache clears the memoized endpoint manifest. The only
// production call site is pkg/cache/reconciler.go (reconcileAll's pass entry,
// once per pass — NOT reconcileTarget, which runs once per target). This
// intentionally diverges from ResetStreamsCache, which still resets
// per-target: #73 found that a per-target netboot.xyz reset re-fetched the
// ~35KB manifest once per tool target instead of once per tick. Do NOT
// "restore parity" by moving this call back into reconcileTarget — that is
// exactly the regression #73 fixed.
//
// The reverify path (pkg/http/api_cache.go) deliberately does NOT reset this
// memo, unlike ResetStreamsCache which it does reset there: VerifyVersion
// short-circuits the tool family before it ever calls Artifacts, the only
// reader of this memo, so a tool reverify never observes stale data. Do not
// "helpfully" restore a reset call there.
func ResetNetbootxyzCache() {
	netbootxyzCache.Lock()
	netbootxyzCache.endpoints = nil
	netbootxyzCache.Unlock()
}

// releaseTag extracts the release tag from an entry path — the last non-empty
// segment, e.g. "/asset-mirror/releases/download/13.01-d20a63ac/" ->
// "13.01-d20a63ac". The tag is booty's on-disk version because it changes
// exactly when the artifacts change, which the pretty version field does not
// (rescatux publishes the literal "current" forever).
func releaseTag(path string) string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return ""
	}
	segs := strings.Split(trimmed, "/")
	last := segs[len(segs)-1]
	if last == "." || last == ".." {
		return ""
	}
	return last
}

// netbootxyzOS implements OS for every netboot.xyz-sourced tool. Per-tool
// variation is DATA (name + arch->endpoint key), so one implementation serves
// all of them and the netboot.xyz plumbing exists exactly once.
//
// endpoints is an explicit arch->key map rather than a "name-{arch}" template
// because upstream keys are irregular: systemrescue-amd64 is per-arch,
// memtest86plus carries no arch, uefishell uses a different token entirely.
type netbootxyzOS struct {
	name      string
	endpoints map[string]string

	// files restricts Artifacts to exactly these filenames instead of every
	// file in the manifest entry. It exists because upstream manifests list
	// files a release does not always publish — netboot.xyz's endpoints.yml
	// lists 7 files for memtest86plus, but its own asset mirror serves only 3
	// at release 8.00-32a14678 — and pkg/cache/reconcile.go's reconcileTarget
	// abandons the WHOLE version if a single artifact 404s, so an unfiltered
	// fetch can permanently prevent that version from ever being marked cached
	// (endless re-download every reconcile pass). It also excludes files no
	// registered arch boots (uefi-shell's arm/aarch64 builds on an amd64-only
	// registration), which would otherwise be cached and served for nothing.
	// Every tool MUST declare its files (D14): there is no "empty means every
	// file in the entry" mode. An empty allowlist is a registration bug, and
	// Artifacts errors rather than silently caching everything. Artifacts also
	// fails loudly if an allowlisted name is absent from the manifest entry,
	// because that means upstream renamed or dropped the exact file the boot
	// script needs.
	files []string

	// large marks allowlisted filenames that must bypass the 5-minute staged
	// download ceiling (D13). Keyed by filename rather than a size threshold
	// because the size is not known until the request is already in flight, and
	// the ceiling is on the whole request. Only Tails needs it today.
	large map[string]bool

	// checksums names the release asset publishing upstream digests for this
	// tool's files, or "" when upstream publishes none — which is seven of the
	// eight tools (verified across both source repos: netbootxyz/asset-mirror
	// publishes sha256-checksums.txt on 76 of 76 Tails releases and on nothing
	// else; netbootxyz/debian-squash, which serves clonezilla and rescatux,
	// publishes no checksums at all).
	//
	// It is deliberately NOT in files: it is verification material — fetched,
	// parsed and discarded, never cached and never served. It is also exempt
	// from the manifest-membership check in Artifacts, because endpoints.yml
	// does not list it. That omission is the entire premise of #76, and this is
	// the one place the design derives an artifact URL from a filename booty
	// hardcodes rather than one upstream declares. The composed URL is still
	// host-pinned to assetBase and the fetched bytes are only ever compared
	// against — never executed, cached, or served.
	checksums string

	// checksumCovers names the files the sidecar MUST list. A declared-covered
	// file absent from the sidecar is an error; any OTHER file's absence is not,
	// because the Tails sidecar legitimately lists only the ISO.
	//
	// What this uniquely guards is a SIDECAR-ONLY DESYNC: the manifest and the
	// release asset still say tails-amd64.iso while the sidecar drops or
	// re-keys that line. Nothing else in the pipeline notices — every other
	// check passes — so the 1.94 GB ISO would silently land not-verifiable,
	// which is the exact silent downgrade fail-loud exists to forbid.
	//
	// On an upstream RENAME, which check fires depends on the manifest:
	//   - manifest tracked the rename -> the manifest-membership check below
	//     errors first ("allowlisted file %q is not in the manifest entry"),
	//     and this field is never reached;
	//   - manifest LAGS the rename -> membership passes, but that release's
	//     sidecar keys the NEW name, so the old name is uncovered and THIS
	//     field errors — before any download, rather than the 404 a naive
	//     reading predicts.
	// Both fail loud. Do not simplify this comment to "only a desync" or to
	// "rename protection" — say which branch.
	//
	// Not derivable from large, even though the two hold identical content
	// today: large means "too big for the staged downloader's 5-minute
	// ceiling", checksumCovers means "upstream promises a digest for this".
	// Different change-drivers — a tool could publish a sidecar covering only
	// small files, or mark a file large that upstream never checksums.
	// Entries must be a subset of files (asserted by a registry test).
	checksumCovers []string
}

func (t netbootxyzOS) Name() string             { return t.name }
func (t netbootxyzOS) Family() Family           { return families["tool"] }
func (t netbootxyzOS) RequiredParams() []string { return nil }

// ValidateVersion enforces path-safety and nothing more. netboot.xyz release
// tags share no grammar (13.01-d20a63ac, edk2-stable202002-a6917535,
// 2025.11_31_x86-64_0.42-bf7a6bdf), so a stricter rule would be fiction — but
// the tag becomes a cache DIRECTORY NAME and arrives from a third party, so
// path-safety is mandatory.
func (t netbootxyzOS) ValidateVersion(v string) error {
	if err := config.ValidatePathSegment(v); err != nil {
		return fmt.Errorf("ostype: %s: invalid release tag %q: %w", t.name, v, err)
	}
	return nil
}

// CompareVersions orders tags lexicographically. netboot.xyz publishes one
// release per endpoint at a time, so ordering only matters once a tag has been
// superseded. NOTE: this misorders at retain > 1 (lexically "9.05" > "13.01");
// tools are declared with retain: 1 and CATALOG.md documents the caveat.
func (t netbootxyzOS) CompareVersions(a, b string) int { return strings.Compare(a, b) }

// entryFor resolves this tool's manifest entry for one arch.
func (t netbootxyzOS) entryFor(ctx context.Context, arch string) (netbootxyzEntry, error) {
	key, ok := t.endpoints[arch]
	if !ok {
		return netbootxyzEntry{}, fmt.Errorf("ostype: %s: no endpoint for arch %q", t.name, arch)
	}
	eps, err := fetchNetbootxyzDoc(ctx)
	if err != nil {
		return netbootxyzEntry{}, err
	}
	e, ok := eps[key]
	if !ok {
		return netbootxyzEntry{}, fmt.Errorf("ostype: %s: endpoint %q not in manifest", t.name, key)
	}
	return e, nil
}

// DiscoverVersions returns the release tag for every arch this tool registers,
// sorted and de-duplicated.
//
// The OS interface gives DiscoverVersions no arch, but netboot.xyz keys
// endpoints by arch — the union is the only honest answer. Every curated tool
// is single-arch (asserted by TestEveryToolIsSingleArch), so the union is one
// tag in practice; Artifacts re-checks the tag against the requested arch.
func (t netbootxyzOS) DiscoverVersions(ctx context.Context, _ map[string]string) ([]string, error) {
	var tags []string
	for arch := range t.endpoints {
		e, err := t.entryFor(ctx, arch)
		if err != nil {
			return nil, err
		}
		tag := releaseTag(e.Path)
		if err := t.ValidateVersion(tag); err != nil {
			return nil, err
		}
		if !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	slices.Sort(tags)
	return tags, nil
}

// Artifacts returns one downloadable per file in t.files (see its doc
// comment); every tool MUST declare its files (D14), there is no "empty means
// every file in the entry" mode. // Verification material: only a tool declaring `checksums` gets a digest, and
// only for files that release's sidecar actually lists. Today that is Tails'
// ISO alone; the other seven tools publish nothing and land not-verifiable
// under every signature policy (accepted risk). SigURL is never set —
// GPG verification of a detached signature over a multi-GB ISO is out of scope.
//
// It REFUSES a version that is not the entry's current tag: the manifest holds
// one path per endpoint, so honouring a stale version is impossible, and
// failing loudly beats writing current bytes into an old version's directory.
func (t netbootxyzOS) Artifacts(ctx context.Context, version, arch string, _ map[string]string) ([]Artifact, error) {
	e, err := t.entryFor(ctx, arch)
	if err != nil {
		return nil, err
	}
	tag := releaseTag(e.Path)
	if err := t.ValidateVersion(tag); err != nil {
		return nil, err
	}
	if version != tag {
		return nil, fmt.Errorf("ostype: %s/%s: requested version %q but upstream now publishes %q; refusing to mix releases", t.name, arch, version, tag)
	}
	// D14: every tool declares its files. There is no "empty means everything"
	// mode — netboot.xyz manifests list files their releases do not publish
	// (memtest86plus: 7 listed, 3 published), and one 404 aborts the whole
	// version forever. An empty allowlist is a registration bug, not a mode.
	if len(t.files) == 0 {
		return nil, fmt.Errorf("ostype: %s: no file allowlist declared (D14)", t.name)
	}
	names := t.files

	base := viper.GetString(config.NetbootxyzAssetBase)

	// D2: fetch the sidecar ONCE, before the file loop, so a multi-file tool
	// pays one ~90-byte GET. Unfetchable or malformed is a LOUD failure, never a
	// silent downgrade to not-verifiable: reconcile.go turns an Artifacts error
	// into "log a warning and skip this version this tick" without touching the
	// row, so fail-loud costs the availability of UPDATES, never of the existing
	// cache.
	//
	// Not memoized: tails is amd64-only at retain 1 and targets are
	// UNIQUE(os, arch, params), so this is roughly one GET per reconcile pass.
	// A second memo would buy that back at the cost of a reset-coupling to
	// ResetNetbootxyzCache and an unspecified failure-memoization policy.
	var sums map[string]string
	if t.checksums != "" {
		su, err := artifactURL(base, e.Path, t.checksums)
		if err != nil {
			return nil, fmt.Errorf("ostype: %s: %w", t.name, err)
		}
		body, ferr := fetchMetadata(ctx, su)
		if ferr != nil {
			return nil, fmt.Errorf("ostype: %s: fetch %s: %w", t.name, t.checksums, ferr)
		}
		parsed, perr := checksum.ParseSums(body)
		if perr != nil {
			return nil, fmt.Errorf("ostype: %s: parse %s: %w", t.name, t.checksums, perr)
		}
		sums = parsed
	}

	out := make([]Artifact, 0, len(names))
	for _, f := range names {
		if !slices.Contains(e.Files, f) {
			return nil, fmt.Errorf("ostype: %s: allowlisted file %q is not in the manifest entry for %s/%s; upstream may have renamed or dropped it", t.name, f, t.name, arch)
		}
		u, err := artifactURL(base, e.Path, f)
		if err != nil {
			return nil, fmt.Errorf("ostype: %s: %w", t.name, err)
		}
		a := Artifact{Filename: f, URL: u, Large: t.large[f]}
		// Lookup is by FILENAME, never "it's the only line". The sidecar has one
		// line today; keying on the name is what makes a rename fail loudly
		// instead of attaching the wrong file's digest to the ISO.
		if d, ok := sums[f]; ok {
			a.SHA256 = d
		} else if slices.Contains(t.checksumCovers, f) {
			return nil, fmt.Errorf(
				"ostype: %s: %q is declared checksum-covered but absent from %s", t.name, f, t.checksums)
		}
		// Any OTHER file absent from the sidecar stays not-verifiable: the Tails
		// sidecar legitimately lists only the ISO.
		out = append(out, a)
	}
	return out, nil
}

// artifactURL composes an asset URL from the configured base and a
// manifest-supplied path.
//
// It validates the PATH, not the composed URL. A composed-URL host check would
// be dead code: because the path is concatenated ONTO assetBase, the authority
// is always assetBase's, so the host can never differ no matter what the
// manifest says. The reachable risk is a path that is itself absolute or
// protocol-relative, which is what is rejected here.
//
// Any check applies to the composed URL only, never a redirect target: GitHub
// release assets 302 to objects.githubusercontent.com and the client follows.
func artifactURL(assetBase, entryPath, file string) (string, error) {
	if assetBase == "" {
		return "", fmt.Errorf("asset base is unset (%s)", config.NetbootxyzAssetBase)
	}
	base, err := url.Parse(assetBase)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("asset base %q is not an absolute URL", assetBase)
	}
	// Reject a manifest path that could relocate the request: an absolute URL,
	// a protocol-relative "//host/...", or a non-rooted path.
	if strings.Contains(entryPath, "://") ||
		strings.HasPrefix(entryPath, "//") ||
		!strings.HasPrefix(entryPath, "/") {
		return "", fmt.Errorf("manifest path %q is not a rooted relative path", entryPath)
	}
	// Normalize the separator rather than assuming a trailing slash. All eight
	// curated endpoints happen to have one, but the manifest does not guarantee
	// it (the non-curated "dts" entry is "/dts/v2.7.1"), and a missing slash
	// would silently produce ".../v2.7.1<file>" instead of ".../v2.7.1/<file>".
	raw := assetBase + strings.TrimSuffix(entryPath, "/") + "/" + file
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("compose url %q: %w", raw, err)
	}
	return u.String(), nil
}
