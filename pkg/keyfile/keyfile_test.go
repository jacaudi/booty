package keyfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boot-ticket-key")
	k1, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != 32 {
		t.Fatalf("len = %d", len(k1))
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	// A restart reads the same key back, so tickets issued before it still verify.
	k2, err := LoadOrCreate(path)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Errorf("second call: %x, %v; want %x", k2, err, k1)
	}

	bad := filepath.Join(t.TempDir(), "short")
	if werr := os.WriteFile(bad, make([]byte, 31), 0o600); werr != nil {
		t.Fatal(werr)
	}
	_, kerr := LoadOrCreate(bad)
	if kerr == nil {
		t.Fatal("31-byte key file: want error")
	}
	if !strings.Contains(kerr.Error(), bad) {
		t.Errorf("error %q does not name the path %q", kerr, bad)
	}
}

func TestLoadOrCreateLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreate(filepath.Join(dir, "k")); err != nil {
		t.Fatal(err)
	}
	// Rename onto a non-empty directory fails; the error must not mention a closed file.
	bad := filepath.Join(dir, "d")
	if err := os.Mkdir(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := writeKey(bad, make([]byte, 32))
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "already closed") {
		t.Errorf("spurious close error: %v", err)
	}
	prefix := "." + filepath.Base(bad) + "-"
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
