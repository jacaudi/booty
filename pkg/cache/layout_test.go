package cache

import (
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestCacheDirAndURLShareSegments(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, "/data")

	dir := cacheDir("talos", "schem1", "amd64", "v1.10.5")
	if want := filepath.Join("/data", "cache", "talos", "schem1", "amd64", "v1.10.5"); dir != want {
		t.Errorf("cacheDir = %q, want %q", dir, want)
	}
	url := CacheURLBase("10.0.0.1", "talos", "schem1", "amd64", "v1.10.5")
	if want := "10.0.0.1/data/cache/talos/schem1/amd64/v1.10.5"; url != want {
		t.Errorf("CacheURLBase = %q, want %q", url, want)
	}
}

func TestNameBridge_FedoraCoreOS(t *testing.T) {
	if got := canonicalToCacheName("fedora-coreos"); got != "coreos" {
		t.Errorf("canonicalToCacheName(fedora-coreos) = %q, want coreos", got)
	}
	if got := cacheNameToCanonical("coreos"); got != "fedora-coreos" {
		t.Errorf("cacheNameToCanonical(coreos) = %q, want fedora-coreos", got)
	}
	// flatcar and talos are identity in both directions.
	for _, n := range []string{"flatcar", "talos"} {
		if canonicalToCacheName(n) != n || cacheNameToCanonical(n) != n {
			t.Errorf("bridge should be identity for %q", n)
		}
	}
}

func TestParamSegment(t *testing.T) {
	if got := paramSegment(map[string]string{"schematic": "abc"}); got != "abc" {
		t.Errorf("paramSegment(schematic) = %q, want abc", got)
	}
	if got := paramSegment(nil); got != "-" {
		t.Errorf("paramSegment(nil) = %q, want -", got)
	}
	if got := paramSegment(map[string]string{"channel": "stable"}); got != "-" {
		t.Errorf("paramSegment(channel-only) = %q, want -", got)
	}
}

func TestEncodeDecodeParams_CanonicalAndRoundTrip(t *testing.T) {
	// Go's json.Marshal emits map keys sorted, so equal param sets always
	// encode to equal strings (required by targets UNIQUE(os,arch,params)).
	a, err := encodeParams(map[string]string{"schematic": "x", "channel": "stable"})
	if err != nil {
		t.Fatalf("encodeParams: %v", err)
	}
	b, _ := encodeParams(map[string]string{"channel": "stable", "schematic": "x"})
	if a != b {
		t.Errorf("encodeParams not canonical: %q vs %q", a, b)
	}
	if got, _ := encodeParams(nil); got != "{}" {
		t.Errorf("encodeParams(nil) = %q, want {}", got)
	}
	round, err := decodeParams(a)
	if err != nil {
		t.Fatalf("decodeParams: %v", err)
	}
	if round["schematic"] != "x" || round["channel"] != "stable" {
		t.Errorf("decodeParams round-trip = %v", round)
	}
}
