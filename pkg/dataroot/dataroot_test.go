package dataroot

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTree builds files and symlinks under a temp dir and returns an [os.Root]
// on it. files maps a relative path to its content; links maps a relative path
// to its (verbatim) symlink target.
func newTree(t *testing.T, files, links map[string]string) *os.Root {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for rel, target := range links {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestResolve(t *testing.T) {
	root := newTree(t,
		map[string]string{
			"api-token":            "secret",
			"public/ok.txt":        "ok",
			"public/sub/deep/f.md": "deep",
		},
		map[string]string{
			"public/rel":    "ok.txt",
			"public/tok":    "../api-token",
			"public/up":     "..",
			"public/hop1":   "hop2",
			"public/hop2":   "../api-token",
			"public/dir":    "sub/deep",
			"public/abs":    "/etc/passwd",
			"public/out":    "../../outside",
			"public/loop1":  "loop2",
			"public/loop2":  "loop1",
			"public/selfie": "selfie",
		})

	cases := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{name: "public/ok.txt", want: "public/ok.txt"},
		{name: "public/rel", want: "public/ok.txt"},
		{name: "public/tok", want: "api-token"},
		{name: "public/hop1", want: "api-token"},
		{name: "public/up/api-token", want: "api-token"},
		{name: "public/up/public/ok.txt", want: "public/ok.txt"},
		{name: "public/dir/f.md", want: "public/sub/deep/f.md"},
		// ".." after a symlink pops the link's TARGET, as the kernel does.
		{name: "public/dir/../deep/f.md", want: "public/sub/deep/f.md"},
		{name: "public/sub/../ok.txt", want: "public/ok.txt"},
		{name: "public//./ok.txt", want: "public/ok.txt"},
		{name: "public/abs", wantErr: true},
		{name: "public/out", wantErr: true},
		{name: "public/up/..", wantErr: true},
		{name: "..", wantErr: true},
		{name: "public/loop1", wantErr: true},
		{name: "public/selfie", wantErr: true},
		{name: "public/missing", wantErr: true},
		{name: "public/ok.txt/x", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolve(root, c.name)
			if c.wantErr {
				if err == nil {
					t.Fatalf("resolve(%q) = %q, want error", c.name, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve(%q): %v", c.name, err)
			}
			if got != c.want {
				t.Errorf("resolve(%q) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

func TestOpen(t *testing.T) {
	root := newTree(t,
		map[string]string{
			"api-token":     "secret",
			"public/ok.txt": "ok",
		},
		map[string]string{
			"public/rel": "ok.txt",
			"public/tok": "../api-token",
			"public/up":  "..",
		})
	underPublic := func(p string) bool { return strings.HasPrefix(p, "public/") }

	cases := []struct {
		name string
		want string // content; "" means refused with os.ErrNotExist
	}{
		{"public/ok.txt", "ok"},
		{"public/rel", "ok"},
		{"public/up/public/ok.txt", "ok"},
		{"api-token", ""},
		{"public/tok", ""},
		{"public/up/api-token", ""},
		{"public/missing", ""},
	}
	for _, c := range cases {
		f, err := Open(root, c.name, underPublic)
		if c.want == "" {
			if !errors.Is(err, os.ErrNotExist) {
				t.Errorf("Open(%q): err = %v, want os.ErrNotExist", c.name, err)
			}
			if f != nil {
				f.Close()
				t.Errorf("Open(%q) returned a file, want none", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("Open(%q): %v", c.name, err)
			continue
		}
		b, _ := io.ReadAll(f)
		f.Close()
		if string(b) != c.want {
			t.Errorf("Open(%q) = %q, want %q", c.name, b, c.want)
		}
	}
}

// TestOpen_PredicateSeesBothPaths: the predicate judges the requested name AND
// the resolved path, in that order, so a non-allowlisted request never costs a
// filesystem walk.
func TestOpen_PredicateSeesBothPaths(t *testing.T) {
	root := newTree(t,
		map[string]string{"public/ok.txt": "ok"},
		map[string]string{"public/rel": "ok.txt"})

	var seen []string
	f, err := Open(root, "public/rel", func(p string) bool {
		seen = append(seen, p)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	want := []string{"public/rel", "public/ok.txt"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("predicate saw %q, want %q", seen, want)
	}

	seen = nil
	if _, err := Open(root, "nope", func(p string) bool {
		seen = append(seen, p)
		return false
	}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want os.ErrNotExist", err)
	}
	if len(seen) != 1 {
		t.Errorf("predicate saw %q, want exactly the requested name", seen)
	}
}
