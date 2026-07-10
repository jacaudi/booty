package tftp

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
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
	// seed a cached version so cache.NewestCached resolves it
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

// dispatchKind classifies what readHandler would serve for a given host, by
// running the same selection logic. The production code factors this into a
// helper bootDispatch(host) returning ("holding"|"assigned", osToLoad) so the
// test asserts the state machine directly.
func TestBootDispatchStateMachine(t *testing.T) {
	cases := []struct {
		name     string
		host     *hardware.Host
		wantKind string
		wantOS   string
	}{
		{"no host -> holding", nil, "holding", ""},
		{"unapproved -> holding", &hardware.Host{OS: "flatcar"}, "holding", ""},
		{"approved assigned -> boots assigned_os", &hardware.Host{Approved: true, BootMode: "assigned", AssignedOS: "talos", OS: "talos"}, "assigned", "talos"},
		{"approved assigned empty -> falls back to host.OS", &hardware.Host{Approved: true, BootMode: "assigned", AssignedOS: "", OS: "flatcar"}, "assigned", "flatcar"},
		{"approved menu -> menu", &hardware.Host{Approved: true, BootMode: "menu", OS: "flatcar"}, "menu", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, osToLoad := bootDispatch(tc.host)
			if kind != tc.wantKind || osToLoad != tc.wantOS {
				t.Fatalf("bootDispatch = (%q,%q), want (%q,%q)", kind, osToLoad, tc.wantKind, tc.wantOS)
			}
		})
	}
}

// A migrated/assigned host must produce the SAME tokens the pre-P1c path did:
// bootTokens keyed on the assigned OS == bootTokens keyed on host.OS.
func TestAssignedTokensMatchLegacy(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	viper.Set(config.FlatcarArchitecture, "amd64")

	host := &hardware.Host{Approved: true, BootMode: "assigned", AssignedOS: "flatcar", OS: "flatcar"}
	_, osToLoad := bootDispatch(host)
	got := bootTokens(osToLoad, "10.0.0.1", host)
	legacy := bootTokens("flatcar", "10.0.0.1", host)
	if got["[[flatcar-baseurl]]"] != legacy["[[flatcar-baseurl]]"] || got["[[flatcar-version]]"] != legacy["[[flatcar-version]]"] {
		t.Fatalf("assigned tokens diverge from legacy: %v vs %v", got, legacy)
	}
}

// TestBootTokensByteIdentical is a characterization guard: it pins the exact
// output of bootTokens for all three OSes so the bootTokensFor extraction
// (Task 2) cannot silently change the assigned-boot token map. #48 is a spec
// change for flatcar/coreos (channel-segmented caching), so their expectations
// are UPDATED here; talos stays byte-identical to the #44 guard's original
// promise.
func TestBootTokensByteIdentical(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosSchematic, "schemX")

	seed := func(cacheName, seg, arch, ver string) {
		if err := os.MkdirAll(filepath.Join(root, "cache", cacheName, seg, arch, ver), 0o755); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// #48: flatcar/coreos artifacts live under the CHANNEL segment now.
	seed("flatcar", "stable", "amd64", "3815.2.0")
	seed("coreos", "stable", "x86_64", "40.20240101.3.0")
	seed("talos", "schemX", "amd64", "v1.10.5")

	const server = "10.0.0.1"
	cases := []struct {
		os   string
		want map[string]string
	}{
		{"flatcar", map[string]string{
			"[[server]]":          server,
			"[[flatcar-arch]]":    "amd64",
			"[[flatcar-version]]": "3815.2.0",
			"[[flatcar-baseurl]]": "http://" + cache.CacheURLBase(server, "flatcar", "stable", "amd64", "3815.2.0"),
		}},
		// [[coreos-channel]] is GONE (dead token: coreos.ipxe set ${STREAM} but
		// never used it); the baseurl carries the channel segment instead.
		{"coreos", map[string]string{
			"[[server]]":         server,
			"[[coreos-arch]]":    "x86_64",
			"[[coreos-version]]": "40.20240101.3.0",
			"[[coreos-baseurl]]": "http://" + cache.CacheURLBase(server, "coreos", "stable", "x86_64", "40.20240101.3.0"),
		}},
		// Talos: byte-identical to pre-#48 (the #44 guard's original promise).
		{"talos", map[string]string{
			"[[server]]":          server,
			"[[talos-schematic]]": "schemX",
			"[[talos-arch]]":      "amd64",
			"[[talos-version]]":   "v1.10.5",
			"[[talos-baseurl]]":   "http://" + cache.CacheURLBase(server, "talos", "schemX", "amd64", "v1.10.5"),
		}},
	}
	for _, tc := range cases {
		got := bootTokens(tc.os, server, nil)
		if !maps.Equal(got, tc.want) {
			t.Errorf("%s: bootTokens = %v, want %v", tc.os, got, tc.want)
		}
	}
}

// TestBootTokensAssignedParamsChannelOverride: the flatcar/coreos arms resolve
// channel exactly the way the talos arm resolves schematic — host override
// (AssignedParams, the P1c field), else flag. #48 stops these arms ignoring it.
func TestBootTokensAssignedParamsChannelOverride(t *testing.T) {
	viper.Reset()
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.FlatcarChannel, "stable")

	if err := os.MkdirAll(filepath.Join(root, "cache", "flatcar", "beta", "amd64", "4300.1.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := &hardware.Host{AssignedParams: `{"channel":"beta"}`}
	tokens := bootTokens("flatcar", "10.0.0.1", host)
	if tokens["[[flatcar-version]]"] != "4300.1.0" {
		t.Errorf("version = %q, want 4300.1.0 (newest under the OVERRIDE channel)", tokens["[[flatcar-version]]"])
	}
	want := "http://" + cache.CacheURLBase("10.0.0.1", "flatcar", "beta", "amd64", "4300.1.0")
	if tokens["[[flatcar-baseurl]]"] != want {
		t.Errorf("baseurl = %q, want %q", tokens["[[flatcar-baseurl]]"], want)
	}

	// Malformed AssignedParams: ignored (flag fallback), never a panic.
	bad := &hardware.Host{AssignedParams: `{not json`}
	if got := bootTokens("flatcar", "10.0.0.1", bad); got["[[flatcar-baseurl]]"] == want {
		t.Error("malformed AssignedParams must fall back to the flag channel")
	}
}

// TestCoreOSTemplateChannelFreeAndDotKernel: the dead [[coreos-channel]] token
// and its `set STREAM` line are removed, and the kernel filename uses the dot
// form the #48 Artifacts fix caches (dash-form would 404 at boot).
func TestCoreOSTemplateChannelFreeAndDotKernel(t *testing.T) {
	tmpl := PXEConfig["coreos.ipxe"]
	if strings.Contains(tmpl, "[[coreos-channel]]") || strings.Contains(tmpl, "STREAM") {
		t.Errorf("coreos.ipxe must not reference the dead channel token/STREAM var:\n%s", tmpl)
	}
	if !strings.Contains(tmpl, "live-kernel.${ARCH}") {
		t.Errorf("coreos.ipxe kernel line must use the dot form live-kernel.${ARCH}:\n%s", tmpl)
	}
	if strings.Contains(tmpl, "live-kernel-${ARCH}") {
		t.Errorf("coreos.ipxe still uses the dash kernel form:\n%s", tmpl)
	}
}

func TestHoldingTemplateExists(t *testing.T) {
	tmpl, ok := PXEConfig["holding.ipxe"]
	if !ok {
		t.Fatalf("holding.ipxe template missing")
	}
	// The holding loop must re-chain over TFTP (booty.ipxe is TFTP-only; there
	// is no HTTP /booty.ipxe route — the / catch-all 302s to /ui/).
	if !strings.Contains(tmpl, "tftp://[[server-ip]]/booty.ipxe") {
		t.Errorf("holding.ipxe must chain via tftp://[[server-ip]]/booty.ipxe, got:\n%s", tmpl)
	}
	if strings.Contains(tmpl, "http://[[server]]/booty.ipxe") {
		t.Errorf("holding.ipxe must NOT chain via http://[[server]]/booty.ipxe (no HTTP route exists), got:\n%s", tmpl)
	}
}

func TestBootTokensTalosMemberUsesClusterPin(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	viper.Set(config.TalosSchematic, "defaultschematic")
	viper.Set(config.TalosArchitecture, "amd64")

	s, err := db.Open(filepath.Join(root, "booty.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { SetStore(nil); s.Close() })
	SetStore(s)
	// hardware.GetMacAddress below reads hardware's own package-level store
	// handle (separate from tftp's); wire it too, mirroring
	// pkg/http/serving_test.go's servingStore helper.
	hardware.SetStore(s)
	t.Cleanup(func() { hardware.SetStore(nil) })

	const mac = "aa:bb:cc:dd:ee:90"
	if err := s.UpsertHost(db.Host{MAC: mac, OS: "talos", Schematic: "schemZ"}); err != nil {
		t.Fatal(err)
	}
	cid, err := s.CreateCluster("pinned", "https://10.0.0.10:6443", "v1.13.5", "v1.34.0", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostCluster(mac, &cid); err != nil {
		t.Fatal(err)
	}
	// Seed a NEWER cached version so NewestCached would win if the pin were ignored.
	if err := os.MkdirAll(filepath.Join(root, "cache", "talos", "schemZ", "amd64", "v1.13.9"), 0o755); err != nil {
		t.Fatal(err)
	}

	host, err := hardware.GetMacAddress(mac)
	if err != nil {
		t.Fatal(err)
	}
	tokens := bootTokens("talos", "10.0.0.1", host)
	if tokens["[[talos-version]]"] != "v1.13.5" {
		t.Fatalf("member must boot the pinned version v1.13.5, got %q", tokens["[[talos-version]]"])
	}
	// The baseurl must carry the pinned version too (boot + install aligned).
	if !strings.Contains(tokens["[[talos-baseurl]]"], "v1.13.5") {
		t.Fatalf("baseurl must carry the pin: %q", tokens["[[talos-baseurl]]"])
	}
}

func TestBootTokensTalosNonMemberUsesNewestCached(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	viper.Set(config.TalosSchematic, "defaultschematic")
	viper.Set(config.TalosArchitecture, "amd64")
	SetStore(nil) // no store: non-member path must not crash

	if err := os.MkdirAll(filepath.Join(root, "cache", "talos", "schemZ", "amd64", "v1.13.9"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := &hardware.Host{OS: "talos", Schematic: "schemZ"} // no ClusterID
	tokens := bootTokens("talos", "10.0.0.1", host)
	if tokens["[[talos-version]]"] != "v1.13.9" {
		t.Fatalf("non-member must use NewestCached v1.13.9, got %q", tokens["[[talos-version]]"])
	}
}
