package tftp

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// newDataTree builds the fixture tree and returns an [os.Root] on its dataDir.
func newDataTree(t *testing.T) *os.Root {
	t.Helper()
	base := t.TempDir()
	dataDir := filepath.Join(base, "dataDir")
	write := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dataDir, "public", "ok.txt"), "ok")
	write(filepath.Join(dataDir, "api-token"), "secret")
	write(filepath.Join(dataDir, "undionly.kpxe"), "bios")
	write(filepath.Join(base, "outside", "secret.txt"), "outside")
	if err := os.MkdirAll(filepath.Join(dataDir, "public", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"escape": "../../outside/secret.txt",
		"abs":    filepath.Join(dataDir, "public", "ok.txt"),
		"rel":    "ok.txt",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dataDir, "public", name)); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

func TestOpenDataFile(t *testing.T) {
	root := newDataTree(t)
	top := []string{"undionly.kpxe"}

	cases := []struct {
		name string
		want string // file content; "" means must be refused
	}{
		{"public/ok.txt", "ok"},
		{"public/rel", "ok"},
		{"undionly.kpxe", "bios"},
		{"public/sub", ""},
		{"public/escape", ""},
		{"public/abs", ""},
		{"api-token", ""},
		{"public/ok.txt\x00.ipxe", ""},
		{"/public/ok.txt", ""},
		{"public/missing", ""},
	}
	for _, c := range cases {
		f, openErr := openDataFile(root, c.name, top)
		if c.want == "" {
			if openErr == nil || !errors.Is(openErr, os.ErrNotExist) {
				t.Errorf("%q: err = %v, want os.ErrNotExist", c.name, openErr)
			}
			if f != nil {
				f.Close()
			}
			continue
		}
		if openErr != nil {
			t.Fatalf("%q: %v", c.name, openErr)
		}
		b, _ := io.ReadAll(f)
		f.Close()
		if string(b) != c.want {
			t.Errorf("%q: got %q, want %q", c.name, b, c.want)
		}
	}
}
