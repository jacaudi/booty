package versions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestCoreOSCleanupRemovesOldVersionDir(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	viper.Set(config.CoreOSArchitecture, "x86_64")

	old := cacheDir("coreos", "-", "x86_64", "39.0.0")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if err := os.WriteFile(filepath.Join(old, "fedora-coreos-39.0.0-live-kernel-x86_64"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	if err := removeVersionDir("coreos", "-", "x86_64", "39.0.0"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old coreos version dir survived cleanup")
	}
}
