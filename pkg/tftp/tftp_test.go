package tftp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
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

func TestApplyTokens(t *testing.T) {
	got := applyTokens("a [[x]] b [[y]]", map[string]string{"[[x]]": "1", "[[y]]": "2"})
	if got != "a 1 b 2" {
		t.Errorf("applyTokens = %q, want %q", got, "a 1 b 2")
	}
}

func TestBootTokensHasNoMenuDefault(t *testing.T) {
	viper.Reset()
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.FlatcarArchitecture, "amd64")

	tokens := bootTokens("flatcar", "10.0.0.1", nil)
	if _, ok := tokens["[[menu-default]]"]; ok {
		t.Errorf("[[menu-default]] token should be gone, got: %v", tokens)
	}
	if tokens["[[server]]"] != "10.0.0.1" {
		t.Errorf("[[server]] = %q, want 10.0.0.1", tokens["[[server]]"])
	}
}

func TestBootTokensTalosUsesHostSchematic(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	viper.Set(config.TalosSchematic, "defaultschematic")
	viper.Set(config.TalosArchitecture, "amd64")

	host := &hardware.Host{OS: "talos", Schematic: "customschematic"}
	tokens := bootTokens("talos", "10.0.0.1", host)

	if tokens["[[talos-schematic]]"] != "customschematic" {
		t.Errorf("schematic = %q, want customschematic", tokens["[[talos-schematic]]"])
	}
	if tokens["[[talos-arch]]"] != "amd64" {
		t.Errorf("arch token missing/wrong: %v", tokens)
	}
	if _, ok := tokens["[[talos-version]]"]; !ok {
		t.Errorf("talos-version token absent: %v", tokens)
	}
}

func TestBootTokensTalosBaseURL(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	viper.Set(config.TalosSchematic, "schem1")
	viper.Set(config.TalosArchitecture, "amd64")
	// seed a cached version so NewestCachedTalos resolves it
	if err := os.MkdirAll(filepath.Join(root, "cache", "talos", "schem1", "amd64", "v1.10.5"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tokens := bootTokens("talos", "10.0.0.1", nil)
	want := "http://" + cache.CacheURLBase("10.0.0.1", "talos", "schem1", "amd64", "v1.10.5")
	if tokens["[[talos-baseurl]]"] != want {
		t.Errorf("[[talos-baseurl]] = %q, want %q", tokens["[[talos-baseurl]]"], want)
	}
}

func TestBootTokensTalosFallsBackToDefaultSchematic(t *testing.T) {
	viper.Reset()
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.TalosSchematic, "defaultschematic")
	viper.Set(config.TalosArchitecture, "amd64")

	// nil host → default schematic; nothing cached → empty version token.
	tokens := bootTokens("talos", "10.0.0.1", nil)
	if tokens["[[talos-schematic]]"] != "defaultschematic" {
		t.Errorf("schematic = %q, want defaultschematic", tokens["[[talos-schematic]]"])
	}
	if tokens["[[talos-version]]"] != "" {
		t.Errorf("talos-version = %q, want empty (nothing cached)", tokens["[[talos-version]]"])
	}

	// host present but no schematic set → still the default.
	tokens = bootTokens("talos", "10.0.0.1", &hardware.Host{OS: "talos"})
	if tokens["[[talos-schematic]]"] != "defaultschematic" {
		t.Errorf("empty-schematic host: got %q, want defaultschematic", tokens["[[talos-schematic]]"])
	}
}
