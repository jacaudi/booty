package tftp

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	// safeJoin reads the package-level absDataDir.
	prev := absDataDir
	absDataDir = abs
	t.Cleanup(func() { absDataDir = prev })

	cases := []struct {
		name      string
		requested string
		wantErr   bool
	}{
		{"simple file", "flatcar_production_pxe.vmlinuz", false},
		{"subdir file", "pxelinux.cfg/default", false},
		{"empty", "", false}, // resolves to absDataDir itself; os.Open would fail later — OK here
		{"dot", ".", false},  // same
		{"double slash", "a//b", false},
		{"parent traversal", "../etc/passwd", true},
		{"deep parent traversal", "a/../../etc/passwd", true},
		{"absolute path", "/etc/passwd", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safeJoin(tc.requested)
			if tc.wantErr {
				if !errors.Is(err, errPathEscapes) {
					t.Errorf("safeJoin(%q) err = %v, want errPathEscapes", tc.requested, err)
				}
				return
			}
			if err != nil {
				t.Errorf("safeJoin(%q) err = %v, want nil", tc.requested, err)
				return
			}
			// Successful resolution must stay under the root.
			if got != abs && !strings.HasPrefix(got, abs+string(filepath.Separator)) {
				t.Errorf("safeJoin(%q) = %q, escapes root %q", tc.requested, got, abs)
			}
		})
	}
}
