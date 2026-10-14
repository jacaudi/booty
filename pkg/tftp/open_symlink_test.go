package tftp

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenDataFile_SymlinkResolution: the allowlist judges where a name
// RESOLVES, not just what was requested. os.Root follows a relative symlink
// that stays inside dataDir, so a link planted in public/ (or in an allowlisted
// ancestor directory) must not reach api-token, config/ or an in-flight
// download. Links whose targets are themselves allowlisted keep working.
func TestOpenDataFile_SymlinkResolution(t *testing.T) {
	root := newDataTree(t)
	dir := root.Name()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("public/other.txt", "other")
	write("cache/talos/kernel", "kernel")
	write("cache/talos/kernel.partial", "half")
	write("config/ignition.yaml", "CONFIG")
	for link, target := range map[string]string{
		"public/tok":       "../api-token",
		"public/up":        "..",
		"public/cfgdir":    "../config",
		"public/hop1":      "hop2",
		"public/hop2":      "../api-token",
		"public/loop1":     "loop2",
		"public/loop2":     "loop1",
		"public/inflight":  "../cache/talos/kernel.partial",
		"public/samelevel": "other.txt",
		"public/tocache":   "../cache/talos/kernel",
		"ipxe.efi":         "api-token",
		"cache/back":       "../public/other.txt",
	} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	top := []string{"undionly.kpxe", "ipxe.efi"}

	cases := []struct {
		name string
		want string // file content; "" means must be refused
	}{
		// Refused: resolves to a non-allowlisted file.
		{"public/tok", ""},
		{"public/up/api-token", ""},
		{"public/cfgdir/ignition.yaml", ""},
		{"public/hop1", ""},
		{"public/loop1", ""},
		{"public/inflight", ""},
		{"ipxe.efi", ""},
		// Served: resolves to an allowlisted file.
		{"public/samelevel", "other"},
		{"public/tocache", "kernel"},
		{"public/up/public/ok.txt", "ok"},
		{"cache/back", "other"},
		{"cache/talos/kernel", "kernel"},
		{"public/ok.txt", "ok"},
	}
	for _, c := range cases {
		f, openErr := openDataFile(root, c.name, top)
		if c.want == "" {
			if openErr == nil || !errors.Is(openErr, os.ErrNotExist) {
				t.Errorf("%q: err = %v, want os.ErrNotExist", c.name, openErr)
			}
			if f != nil {
				b, _ := io.ReadAll(f)
				f.Close()
				t.Errorf("%q: served %q, want refusal", c.name, b)
			}
			continue
		}
		if openErr != nil {
			t.Errorf("%q: %v", c.name, openErr)
			continue
		}
		b, _ := io.ReadAll(f)
		f.Close()
		if string(b) != c.want {
			t.Errorf("%q: got %q, want %q", c.name, b, c.want)
		}
	}
}
