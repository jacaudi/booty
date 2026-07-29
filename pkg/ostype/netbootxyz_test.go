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
		"":  "",
		"/": "",
	}
	for in, want := range cases {
		if got := releaseTag(in); got != want {
			t.Errorf("releaseTag(%q) = %q, want %q", in, got, want)
		}
	}
}
