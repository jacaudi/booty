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
	if got := CacheNameToCanonical("coreos"); got != "fedora-coreos" {
		t.Errorf("CacheNameToCanonical(coreos) = %q, want fedora-coreos", got)
	}
	// flatcar and talos are identity in both directions.
	for _, n := range []string{"flatcar", "talos"} {
		if canonicalToCacheName(n) != n || CacheNameToCanonical(n) != n {
			t.Errorf("bridge should be identity for %q", n)
		}
	}
}

func TestParamSegmentPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{"schematic wins", map[string]string{"schematic": "abc123"}, "abc123"},
		{"channel when no schematic", map[string]string{"channel": "beta"}, "beta"},
		{"schematic beats channel (theoretical: no OS carries both)", map[string]string{"schematic": "abc", "channel": "beta"}, "abc"},
		{"empty map", map[string]string{}, "-"},
		{"nil map", nil, "-"},
		{"empty channel value", map[string]string{"channel": ""}, "-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := paramSegment(tc.params); got != tc.want {
				t.Errorf("paramSegment(%v) = %q, want %q", tc.params, got, tc.want)
			}
		})
	}
}

func TestValidatePathParam(t *testing.T) {
	valid := []string{"stable", "beta", "alpha", "next", "testing", "lts-2024", "3033.2.0", "a.b-c", "a..b", "x86_64", "arm64"}
	for _, v := range valid {
		if err := ValidatePathParam(v); err != nil {
			t.Errorf("ValidatePathParam(%q) = %v, want nil", v, err)
		}
	}
	invalid := []string{"", "..", ".", "a/b", "../etc", ".hidden", "-lead", "UPPER", "sp ace", "a\\b", "_lead"}
	for _, v := range invalid {
		if err := ValidatePathParam(v); err == nil {
			t.Errorf("ValidatePathParam(%q) = nil, want error", v)
		}
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
