package ostype

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

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

// ResetNetbootxyzCache clears the memoized endpoint manifest. Called from
// pkg/cache/reconciler.go (reconcileAll's pass entry, once per pass — NOT from
// reconcileTarget, which runs once per target) and pkg/http/api_cache.go
// (before each reverify). This intentionally diverges from ResetStreamsCache,
// which still resets per-target: #73 found that a per-target netboot.xyz reset
// re-fetched the ~35KB manifest once per tool target instead of once per tick.
// Do NOT "restore parity" by moving this call back into reconcileTarget — that
// is exactly the regression #73 fixed.
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

// Artifacts returns one downloadable per file in the entry. No SHA256/SigURL:
// netboot.xyz publishes neither, so artifacts land not-verifiable under every
// signature policy (design §8.5, accepted risk).
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
	base := viper.GetString(config.NetbootxyzAssetBase)
	out := make([]Artifact, 0, len(e.Files))
	for _, f := range e.Files {
		u, err := artifactURL(base, e.Path, f)
		if err != nil {
			return nil, fmt.Errorf("ostype: %s: %w", t.name, err)
		}
		out = append(out, Artifact{Filename: f, URL: u})
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
