package http

import (
	"flag"
	ignitionConfig "github.com/coreos/ignition/v2/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
)

//nolint:gochecknoglobals // test flag, must be package-level for the flag package
var updateIgnition = flag.Bool("update-ignition", false, "rewrite ignition goldens")

// compareIgnitionGolden compares got with testdata/ignition/<name>, rewriting
// the golden first when -update-ignition is set.
func compareIgnitionGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "ignition", name)
	if *updateIgnition {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s changed.\nwant:\n%s\ngot:\n%s", name, want, got)
	}
}

// TestIgnitionOutputsStable pins the reboot config and the shipped Flatcar
// example so a butane/ignition bump cannot silently change them.
func TestIgnitionOutputsStable(t *testing.T) {
	t.Run("reboot", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(
			filepath.Join(dir, "ignition.yaml"),
			[]byte("variant: fcos\nversion: 1.5.0\n"),
			0o600,
		); err != nil {
			t.Fatalf("write template: %v", err)
		}
		viper.Reset()
		t.Cleanup(viper.Reset)
		viper.Set(config.DataDir, dir)
		viper.Set(config.IgnitionFile, "ignition.yaml")
		viper.Set(config.ServerIP, "127.0.0.1")
		viper.Set(config.ServerHttpPort, "80")

		req := httptest.NewRequest(http.MethodGet, "/ignition", nil)
		req.RemoteAddr = "192.0.2.1:12345"
		rec := httptest.NewRecorder()
		handleIgnitionRequest(ignitionTestStore(t))(rec, req)
		compareIgnitionGolden(t, "reboot.json", rec.Body.Bytes())
	})

	t.Run("example-flatcar", func(t *testing.T) {
		src, err := os.ReadFile("../../examples/config/ignition.yaml")
		if err != nil {
			t.Fatalf("read example: %v", err)
		}
		out, _, _, err := renderConfig(
			"butane",
			src,
			TemplateVars{Hostname: "node1", ServerIP: "10.0.0.1:8080", JoinString: "kubeadm join x"},
		)
		if err != nil {
			t.Fatalf("renderConfig: %v", err)
		}
		compareIgnitionGolden(t, "example-flatcar.json", out)
	})
}

// TestRebootConfigIsValidIgnition parses the config served to unknown hosts with
// Ignition's own parser: a fatal report makes Flatcar/CoreOS stop at an
// Ignition error instead of rebooting.
func TestRebootConfigIsValidIgnition(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ignition.yaml"), []byte("variant: fcos\nversion: 1.5.0\n"), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	viper.Set(config.IgnitionFile, "ignition.yaml")
	viper.Set(config.ServerIP, "127.0.0.1")
	viper.Set(config.ServerHttpPort, "80")

	req := httptest.NewRequest(http.MethodGet, "/ignition", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()
	handleIgnitionRequest(ignitionTestStore(t))(rec, req)

	_, rep, err := ignitionConfig.Parse(rec.Body.Bytes())
	if rep.IsFatal() {
		t.Fatalf("reboot config is not valid Ignition: %v\n%s", err, rep.String())
	}
}
