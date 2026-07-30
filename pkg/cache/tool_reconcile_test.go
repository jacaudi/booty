package cache

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/ostype"
	"github.com/spf13/viper"
)

// TestReconcileToolTargetEndToEnd is the end-to-end proof for netboot.xyz-sourced
// tool/rescue targets: it drives a real reconcileTarget against fixture HTTP
// servers (manifest + asset host, both viper-redirected) and confirms the
// resulting artifact lands on disk at the expected cache path AND is visible
// to the boot menu's own enumeration (ListCached/ValidCachedSelection) — the
// same seam TestReconcileTarget_TalosCachesRetainedAndArchived exercises for
// Talos (pkg/cache/reconcile_test.go).
func TestReconcileToolTargetEndToEnd(t *testing.T) {
	const tag = "8.00-32a14678"

	// Asset server: serves any requested file with a small body.
	assets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("BINARY"))
	}))
	t.Cleanup(assets.Close)

	// Manifest server: one memtest86plus entry whose path yields the tag above.
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("endpoints:\n" +
			"  memtest86plus:\n" +
			"    path: /asset-mirror/releases/download/" + tag + "/\n" +
			"    files:\n" +
			"    - mt86p_x86_64\n" +
			"    os: memtest86-plus\n" +
			"    version: '8.00'\n"))
	}))
	t.Cleanup(manifest.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.NetbootxyzEndpointsURL, manifest.URL)
	viper.Set(config.NetbootxyzAssetBase, assets.URL)
	ostype.ResetNetbootxyzCache()
	t.Cleanup(ostype.ResetNetbootxyzCache)

	store := newReconcileStore(t)
	// Mode and Source are MANDATORY: CreateTarget inserts them verbatim with no
	// defaulting, and the schema CHECKs them (0001_init.sql mode IN
	// ('discovery','manual'); 0007_target_source.sql source IN
	// ('catalog','api','host')). Omitting Mode fails the constraint outright;
	// Mode:"" would also make reconcileTarget skip discovery entirely.
	tid, err := store.CreateTarget(db.Target{
		OS: "memtest86plus", Arch: "amd64", Params: "{}", Enabled: true, RetainN: 1,
		Mode: "discovery", Source: "catalog",
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	target, err := store.GetTarget(tid)
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}

	if err := reconcileTarget(t.Context(), store, 2, *target); err != nil {
		t.Fatalf("reconcileTarget: %v", err)
	}

	// 1. The artifact landed at the tag-named version directory.
	want := filepath.Join(dir, "cache", "memtest86plus", "-", "amd64", tag, "mt86p_x86_64")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("artifact not cached at %s: %v", want, err)
	}

	// 2. The boot menu's own enumeration sees it.
	var found bool
	for _, e := range ListCached() {
		if e.CacheName == "memtest86plus" && e.Version == tag {
			found = true
			if e.Segment != "-" {
				t.Errorf("segment = %q, want \"-\" (tools take no params)", e.Segment)
			}
		}
	}
	if !found {
		t.Errorf("ListCached() does not include the cached tool: %+v", ListCached())
	}

	// 3. The menu-selection boot path accepts the tuple.
	if !ValidCachedSelection("memtest86plus", "-", "amd64", tag) {
		t.Error("ValidCachedSelection rejected the freshly cached tool tuple")
	}
}
