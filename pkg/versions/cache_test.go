package versions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestCacheDirAndURLShareSegments(t *testing.T) {
	viper.Reset()
	viper.Set(config.DataDir, "/data")

	dir := cacheDir("talos", "abc123", "amd64", "v1.10.5")
	url := CacheURLBase("10.0.0.1", "talos", "abc123", "amd64", "v1.10.5")

	wantDir := filepath.Join("/data", "cache", "talos", "abc123", "amd64", "v1.10.5")
	if dir != wantDir {
		t.Errorf("cacheDir = %q, want %q", dir, wantDir)
	}
	wantURL := "10.0.0.1/data/cache/talos/abc123/amd64/v1.10.5"
	if url != wantURL {
		t.Errorf("cacheURLBase = %q, want %q", url, wantURL)
	}
	if filepath.Base(dir) != "v1.10.5" || filepath.Base(url) != "v1.10.5" {
		t.Errorf("dir/url tails diverged: %q vs %q", dir, url)
	}
}

func TestCacheDashSchematicForNonTalos(t *testing.T) {
	viper.Reset()
	viper.Set(config.DataDir, "/data")
	dir := cacheDir("coreos", "-", "x86_64", "40.20240101.3.0")
	want := filepath.Join("/data", "cache", "coreos", "-", "x86_64", "40.20240101.3.0")
	if dir != want {
		t.Errorf("cacheDir = %q, want %q", dir, want)
	}
}

func TestEnsureArtifactDownloadsThenIsIdempotent(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("artifact-bytes"))
	}))
	defer srv.Close()

	dir := cacheDir("talos", "abc", "amd64", "v1.0.0")
	url := srv.URL + "/kernel-amd64"

	if err := ensureArtifact(context.Background(), dir, url); err != nil {
		t.Fatalf("ensureArtifact (first): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kernel-amd64")); err != nil {
		t.Fatalf("artifact not written: %v", err)
	}
	if err := ensureArtifact(context.Background(), dir, url); err != nil {
		t.Fatalf("ensureArtifact (second): %v", err)
	}
	if hits != 1 {
		t.Errorf("server hit %d times, want 1 (second call must be a no-op)", hits)
	}
}

func TestRemoveVersionDir(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)

	dir := cacheDir("flatcar", "-", "amd64", "3000.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := removeVersionDir("flatcar", "-", "amd64", "3000.0.0"); err != nil {
		t.Fatalf("removeVersionDir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("version dir still present after remove")
	}
}
