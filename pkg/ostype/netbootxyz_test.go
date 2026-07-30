package ostype

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

const fixtureDoc = `
endpoints:
  systemrescue-amd64:
    path: /asset-mirror/releases/download/13.01-d20a63ac/
    files:
    - airootfs.sfs
    - initrd
    - vmlinuz
    - archiso_pxe_http
    os: systemrescue
    version: 13.01
    arch: amd64
  memtest86plus:
    path: /asset-mirror/releases/download/8.00-32a14678/
    files:
    - mt86p_x86_64
    os: memtest86-plus
    version: '8.00'
`

func serveFixture(t *testing.T, body string, hits *int32) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	viper.Set(config.NetbootxyzEndpointsURL, srv.URL)
	// The asset base MUST be set too: its default lives in config.LoadConfig(cmd),
	// which no unit test calls, so it would otherwise be "" and every composed
	// artifact URL would come out relative.
	viper.Set(config.NetbootxyzAssetBase, "https://github.com/netbootxyz")
	t.Cleanup(func() {
		viper.Set(config.NetbootxyzEndpointsURL, "")
		viper.Set(config.NetbootxyzAssetBase, "")
	})
	ResetNetbootxyzCache()
	t.Cleanup(ResetNetbootxyzCache)
}

func TestFetchNetbootxyzDocPreservesVersionText(t *testing.T) {
	serveFixture(t, fixtureDoc, nil)
	doc, err := fetchNetbootxyzDoc(context.Background())
	if err != nil {
		t.Fatalf("fetchNetbootxyzDoc: %v", err)
	}
	if got := doc["systemrescue-amd64"].Version; got != "13.01" {
		t.Errorf("version = %q, want \"13.01\"", got)
	}
	if got := len(doc["systemrescue-amd64"].Files); got != 4 {
		t.Errorf("files = %d, want 4", got)
	}
}

func TestFetchNetbootxyzDocToleratesUnknownKeys(t *testing.T) {
	serveFixture(t, fixtureDoc+"\n  extra-thing:\n    path: /x/\n    files: []\n    surprise: yes\n", nil)
	if _, err := fetchNetbootxyzDoc(context.Background()); err != nil {
		t.Fatalf("unknown keys must be tolerated, got %v", err)
	}
}

func TestFetchNetbootxyzDocMemoizes(t *testing.T) {
	var hits int32
	serveFixture(t, fixtureDoc, &hits)
	for range 3 {
		if _, err := fetchNetbootxyzDoc(context.Background()); err != nil {
			t.Fatalf("fetch: %v", err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("upstream hits = %d, want 1", got)
	}
	ResetNetbootxyzCache()
	if _, err := fetchNetbootxyzDoc(context.Background()); err != nil {
		t.Fatalf("fetch after reset: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("hits after reset = %d, want 2", got)
	}
}

func TestReleaseTag(t *testing.T) {
	cases := map[string]string{
		"/asset-mirror/releases/download/13.01-d20a63ac/":       "13.01-d20a63ac",
		"/asset-mirror/releases/download/8.00-32a14678":         "8.00-32a14678",
		"/debian-squash/releases/download/0.72-beta8-2568400c/": "0.72-beta8-2568400c",
		"":                                   "",
		"/":                                  "",
		"/asset-mirror/releases/download/.":  "", // guard: final segment "."
		"/asset-mirror/releases/download/..": "", // guard: final segment ".."
	}
	for in, want := range cases {
		if got := releaseTag(in); got != want {
			t.Errorf("releaseTag(%q) = %q, want %q", in, got, want)
		}
	}
}

var testSysrescue = netbootxyzOS{
	name:      "systemrescue",
	endpoints: map[string]string{"amd64": "systemrescue-amd64"},
}

func TestNetbootxyzOSDiscoverReturnsReleaseTag(t *testing.T) {
	serveFixture(t, fixtureDoc, nil)
	got, err := testSysrescue.DiscoverVersions(context.Background(), nil)
	if err != nil {
		t.Fatalf("DiscoverVersions: %v", err)
	}
	if len(got) != 1 || got[0] != "13.01-d20a63ac" {
		t.Errorf("DiscoverVersions = %#v, want [13.01-d20a63ac]", got)
	}
}

func TestNetbootxyzOSArtifactURLs(t *testing.T) {
	serveFixture(t, fixtureDoc, nil)
	arts, err := testSysrescue.Artifacts(context.Background(), "13.01-d20a63ac", "amd64", nil)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(arts) != 4 {
		t.Fatalf("got %d artifacts, want 4", len(arts))
	}
	want := "https://github.com/netbootxyz/asset-mirror/releases/download/13.01-d20a63ac/vmlinuz"
	var found bool
	for _, a := range arts {
		if a.URL == want {
			found = true
		}
		if a.SHA256 != "" || a.SigURL != "" {
			t.Errorf("%s: netboot.xyz publishes no verification material", a.Filename)
		}
	}
	if !found {
		t.Errorf("no artifact with URL %q; got %+v", want, arts)
	}
}

func TestNetbootxyzOSArtifactsRefusesStaleVersion(t *testing.T) {
	serveFixture(t, fixtureDoc, nil)
	if _, err := testSysrescue.Artifacts(context.Background(), "12.00-deadbeef", "amd64", nil); err == nil {
		t.Fatal("Artifacts accepted a stale version, want error")
	}
}

func TestNetbootxyzOSUnknownArch(t *testing.T) {
	serveFixture(t, fixtureDoc, nil)
	if _, err := testSysrescue.Artifacts(context.Background(), "13.01-d20a63ac", "arm64", nil); err == nil {
		t.Fatal("Artifacts accepted an unregistered arch, want error")
	}
}

func TestNetbootxyzOSRejectsUnsafeTag(t *testing.T) {
	serveFixture(t, `
endpoints:
  systemrescue-amd64:
    path: /asset-mirror/releases/download/..%2f..%2fetc/
    files: [vmlinuz]
    version: evil
`, nil)
	if _, err := testSysrescue.DiscoverVersions(context.Background(), nil); err == nil {
		t.Fatal("DiscoverVersions accepted an unsafe release tag, want error")
	}
}

// The guard validates the manifest-supplied PATH, not the composed URL. A
// composed-URL host check is structurally incapable of failing here, because the
// authority always comes from assetBase — see the comment on artifactURL.
func TestArtifactURLRejectsUnsafePath(t *testing.T) {
	for _, bad := range []string{
		"https://evil.example/x/", // absolute URL
		"//evil.example/x/",       // protocol-relative
		"asset-mirror/x/",         // not rooted
	} {
		if _, err := artifactURL("https://github.com/netbootxyz", bad, "f"); err == nil {
			t.Errorf("artifactURL accepted unsafe path %q, want error", bad)
		}
	}
}

// An unset asset base must FAIL, not silently produce a relative URL.
func TestArtifactURLRequiresAssetBase(t *testing.T) {
	if _, err := artifactURL("", "/asset-mirror/x/", "f"); err == nil {
		t.Fatal("artifactURL accepted an empty asset base, want error")
	}
}

func TestNetbootxyzOSInterfaceBasics(t *testing.T) {
	if testSysrescue.Name() != "systemrescue" {
		t.Errorf("Name() = %q", testSysrescue.Name())
	}
	if testSysrescue.Family().Name != "tool" {
		t.Errorf("Family() = %+v, want tool", testSysrescue.Family())
	}
	if len(testSysrescue.RequiredParams()) != 0 {
		t.Errorf("RequiredParams() = %#v, want empty", testSysrescue.RequiredParams())
	}
	if testSysrescue.CompareVersions("a", "b") >= 0 {
		t.Error("CompareVersions should order lexicographically")
	}
}
