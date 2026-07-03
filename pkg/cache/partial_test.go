package cache

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSweepPartials_RemovesOnlyPartialFiles asserts SweepPartials deletes a
// stray *.partial left by a crashed download (T2/T9 stage artifacts as
// <file>.partial before landing them) while leaving a real, landed artifact
// in the same directory untouched.
func TestSweepPartials_RemovesOnlyPartialFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "talos", "s", "amd64", "v1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "kernel-amd64")
	partial := filepath.Join(dir, "initramfs-amd64.xz.partial")
	if err := os.WriteFile(artifact, []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SweepPartials(root); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Errorf("SweepPartials should have removed %s, stat err = %v", partial, err)
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Errorf("SweepPartials should not touch real artifact %s: %v", artifact, err)
	}
}
