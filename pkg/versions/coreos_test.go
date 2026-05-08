package versions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveOldCoreOSArtifacts_RemovesFromDataDir(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"fedora-coreos-OLD-live-initramfs.x86_64.img",
		"fedora-coreos-OLD-live-kernel-x86_64",
		"fedora-coreos-OLD-live-rootfs.x86_64.img",
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("stale"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", f, err)
		}
	}

	removeOldCoreOSArtifacts(dir, "OLD", "x86_64")

	for _, f := range files {
		path := filepath.Join(dir, f)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, stat err: %v", path, err)
		}
	}
}

func TestRemoveOldCoreOSArtifacts_MissingFilesAreSilent(t *testing.T) {
	dir := t.TempDir()
	// No files seeded; helper should be a no-op (no panic, no error log impacting test).
	removeOldCoreOSArtifacts(dir, "MISSING", "x86_64")
}

func TestRemoveOldCoreOSArtifacts_DoesNotTouchCwd(t *testing.T) {
	cwd := t.TempDir()
	dataDir := t.TempDir()

	cwdFiles := []string{
		"fedora-coreos-OLD-live-initramfs.x86_64.img",
		"fedora-coreos-OLD-live-kernel-x86_64",
		"fedora-coreos-OLD-live-rootfs.x86_64.img",
	}
	for _, f := range cwdFiles {
		if err := os.WriteFile(filepath.Join(cwd, f), []byte("must survive"), 0o644); err != nil {
			t.Fatalf("seed cwd %s: %v", f, err)
		}
	}

	t.Chdir(cwd)

	removeOldCoreOSArtifacts(dataDir, "OLD", "x86_64")

	for _, f := range cwdFiles {
		path := filepath.Join(cwd, f)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("cwd file %s should still exist, stat err: %v", path, err)
		}
	}
}
