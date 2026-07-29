package ostype

import (
	"context"
	"fmt"
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

// ResetNetbootxyzCache clears the memoized endpoint manifest. Called at exactly
// the two sites ResetStreamsCache is: pkg/cache/reconcile.go (top of
// reconcileTarget) and pkg/http/api_cache.go (before each reverify).
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
