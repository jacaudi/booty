package cache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func seedVersionDir(t *testing.T, cacheName, segment, arch, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(cacheRoot(), cacheName, segment, arch, version), 0o755); err != nil {
		t.Fatalf("seed %s: %v", version, err)
	}
}

func TestNewestCached_TalosSemverNewest(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	seedVersionDir(t, "talos", "schem1", "amd64", "v1.9.0")
	seedVersionDir(t, "talos", "schem1", "amd64", "v1.10.5")
	seedVersionDir(t, "talos", "schem1", "amd64", "not-a-version") // ignored

	got := NewestCached("talos", "amd64", map[string]string{"schematic": "schem1"})
	if got != "v1.10.5" {
		t.Errorf("NewestCached(talos) = %q, want v1.10.5", got)
	}
}

func TestNewestCached_CoreOSBridgeDottedNumeric(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	// Disk segment is "coreos"; ordering uses the fedora-coreos ostype.
	seedVersionDir(t, "coreos", "-", "x86_64", "39.20231101.3.0")
	seedVersionDir(t, "coreos", "-", "x86_64", "40.20240101.3.0")

	got := NewestCached("coreos", "x86_64", nil)
	if got != "40.20240101.3.0" {
		t.Errorf("NewestCached(coreos) = %q, want 40.20240101.3.0", got)
	}
}

func TestNewestCached_EmptyWhenNothingCached(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	if got := NewestCached("flatcar", "amd64", nil); got != "" {
		t.Errorf("NewestCached(empty) = %q, want \"\"", got)
	}
}
