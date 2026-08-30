package http

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func withDataDir(t *testing.T) string {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	return dir
}

func TestResolveWithinDataDirAcceptsLegitimateNames(t *testing.T) {
	dir := withDataDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "ignition.yaml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveWithinDataDir("config/ignition.yaml")
	if err != nil {
		t.Fatalf("resolveWithinDataDir: %v", err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(dir, "config", "ignition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved = %q, want %q", got, want)
	}
}

func TestResolveWithinDataDirRejectsEscapes(t *testing.T) {
	withDataDir(t)

	for _, name := range []string{
		"../../etc/passwd",
		"..",
		"../",
		"config/..",
		"config/../../etc/passwd",
		"/etc/passwd",
		"/",
		"",
		"./../../etc/passwd",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := resolveWithinDataDir(name); !errors.Is(err, errPathEscapesDataDir) {
				t.Fatalf("resolveWithinDataDir(%q) err = %v, want errPathEscapesDataDir", name, err)
			}
		})
	}
}

// TestResolveWithinDataDirRejectsASiblingPrefix guards the classic naive
// HasPrefix bug: "<dataDir>-evil" starts with "<dataDir>" as a string but is
// not inside it. The check must be separator-terminated.
func TestResolveWithinDataDirRejectsASiblingPrefix(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	parent := t.TempDir()
	dataDir := filepath.Join(parent, "data")
	sibling := filepath.Join(parent, "data-evil")
	for _, d := range []string{dataDir, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sibling, "secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	viper.Set(config.DataDir, dataDir)

	if _, err := resolveWithinDataDir("../data-evil/secret"); !errors.Is(err, errPathEscapesDataDir) {
		t.Fatalf("sibling-prefix escape err = %v, want errPathEscapesDataDir", err)
	}
}

// TestResolveWithinDataDirRejectsASymlinkEscape covers the case template.ParseFiles
// would otherwise follow: a symlink INSIDE dataDir whose target is outside it.
//
// The target is deliberately RELATIVE (".." segments up to a sibling
// directory, resolved relative to the symlink's own directory), not
// absolute: a relative target is the more common real-world shape (e.g. a
// config tree symlinked in from a sibling checkout), and $GOROOT's
// path/filepath/symlink.go shows walkSymlinks resolves relative targets by
// walking them lexically from the link's directory, exactly like an absolute
// one -- but nothing here pinned that until now. An absolute-target symlink
// remains implicitly covered: this test's own construction is one no-op
// EvalSymlinks call away from being absolute, and resolveWithinDataDir does
// not special-case either form.
func TestResolveWithinDataDirRejectsASymlinkEscape(t *testing.T) {
	dir := withDataDir(t)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "escape.yaml")
	relTarget, err := filepath.Rel(dir, outside)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(relTarget) {
		t.Fatalf("precondition: symlink target %q must be relative", relTarget)
	}
	if err := os.Symlink(relTarget, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := resolveWithinDataDir("escape.yaml"); !errors.Is(err, errPathEscapesDataDir) {
		t.Fatalf("symlink escape err = %v, want errPathEscapesDataDir", err)
	}
}

func TestResolveWithinDataDirReportsAMissingFileDistinctly(t *testing.T) {
	withDataDir(t)
	_, err := resolveWithinDataDir("config/nope.yaml")
	if err == nil {
		t.Fatal("a missing file must return an error")
	}
	if errors.Is(err, errPathEscapesDataDir) {
		t.Fatal("a missing file is NOT an escape; the handler maps them to different statuses")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

// leakSentinel is a path that can only appear in the response if booty read a
// template from OUTSIDE dataDir and rendered it.
const leakSentinel = "/etc/leaked-sentinel-outside-datadir"

// TestIgnitionRefusesATraversingIgnitionFile drives the real handler end to
// end: a host row carrying a traversing IgnitionFile must not read outside
// dataDir.
//
// READ THIS BEFORE SIMPLIFYING THE SETUP. The obvious version of this test --
// set IgnitionFile to "../../etc/passwd" and assert the status is not 200 --
// is INERT. dataDir is a t.TempDir() several levels deep, so that path does
// not resolve to anything, template.ParseFiles already fails, and the handler
// already returns 500 on unmodified main. The assertion holds before and after
// the fix and proves nothing.
//
// So this plants a REAL, VALID butane file one directory above dataDir and
// asserts its rendered output never reaches the client. Measured against
// unmodified main this session: code=200 with the sentinel present in the
// served ignition JSON -- i.e. the traversal is live and exploitable today,
// and this test genuinely fails without the fix.
func TestIgnitionRefusesATraversingIgnitionFile(t *testing.T) {
	s := servingStore(t)
	dataDir := viper.GetString(config.DataDir)

	// Valid butane, so it survives translation and its content is observable
	// in the response. Anything malformed would 500 for the WRONG reason and
	// make the test inert again.
	outside := filepath.Join(filepath.Dir(dataDir), "outside.yaml")
	if err := os.WriteFile(outside, []byte(
		"variant: fcos\nversion: 1.5.0\nstorage:\n  files:\n    - path: "+leakSentinel+
			"\n      contents:\n        inline: LEAKED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	viper.Set(config.IgnitionFile, "config/ignition.yaml")
	writeFile(t, "config/ignition.yaml", "variant: fcos\nversion: 1.5.0\n")

	const mac = "aa:bb:cc:dd:ee:70"
	if err := hardware.WriteMacAddress(mac, hardware.Host{
		MAC: mac, OS: "flatcar", Approved: true, IgnitionFile: "../outside.yaml",
	}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handleIgnitionRequest(s)(rr, httptest.NewRequest(http.MethodGet, "/ignition.json?mac="+mac, nil))

	if strings.Contains(rr.Body.String(), leakSentinel) {
		t.Fatalf("a template from OUTSIDE dataDir was rendered and served: %s", rr.Body.String())
	}
	if rr.Code == 200 {
		t.Fatalf("a traversing IgnitionFile produced a 200: %s", rr.Body.String())
	}
}

// TestIgnitionStillServesALegitimateTemplate is the control. Without it the
// test above passes trivially if resolveWithinDataDir rejects EVERYTHING.
func TestIgnitionStillServesALegitimateTemplate(t *testing.T) {
	s := servingStore(t)
	viper.Set(config.IgnitionFile, "config/ignition.yaml")
	writeFile(t, "config/ignition.yaml",
		"variant: fcos\nversion: 1.5.0\nstorage:\n  files:\n    - path: /etc/legit\n      contents:\n        inline: OK\n")

	const mac = "aa:bb:cc:dd:ee:71"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar", Approved: true}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handleIgnitionRequest(s)(rr, httptest.NewRequest(http.MethodGet, "/ignition.json?mac="+mac, nil))

	if rr.Code != 200 {
		t.Fatalf("a legitimate in-dataDir template = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "/etc/legit") {
		t.Fatalf("the legitimate template was not rendered: %s", rr.Body.String())
	}
}
