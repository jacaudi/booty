package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

// machineConfigTestStore opens a bare empty store so resolveConfig returns
// ok=false and the handler takes the existing file path (rungs 3–4) verbatim.
func machineConfigTestStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// seedApprovedHost registers and approves mac in a fresh hardware store. The
// machineconfig route renders its server-default file only for an approved host
// (#162), so a test whose target is that render must name one. Call it after
// viper.Reset and after DataDir is set.
func seedApprovedHost(t *testing.T, mac string, h hardware.Host) {
	t.Helper()
	viper.Set(config.HardwareMap, "hardware.json")
	if err := hardware.Load(); err != nil {
		t.Fatalf("hardware.Load: %v", err)
	}
	if err := hardware.WriteMacAddress(mac, h); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	approveHost(t, mac)
}

func TestMachineConfig_RendersTemplate(t *testing.T) {
	dir := t.TempDir()
	tmpl := "version: v1alpha1\nmachine:\n  type: worker\n  install:\n    hostname: {{ .Hostname }}\n  serial: {{ .Serial }}\n"
	if err := os.WriteFile(filepath.Join(dir, "machineconfig.yaml"), []byte(tmpl), 0o600); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	viper.Set(config.TalosConfigFile, "machineconfig.yaml")
	viper.Set(config.ServerIP, "10.0.0.1")
	viper.Set(config.ServerHttpPort, "80")
	seedApprovedHost(t, "aa:bb:cc:dd:ee:ff", hardware.Host{})

	req := httptest.NewRequest(http.MethodGet, "/machineconfig?serial=ABC123&mac=aa:bb:cc:dd:ee:ff", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()
	store := machineConfigTestStore(t)

	handleMachineConfigRequest(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("Content-Type = %q, want text/yaml", ct)
	}
	if !strings.Contains(rec.Body.String(), "ABC123") {
		t.Errorf("Serial not rendered: %q", rec.Body.String())
	}
}

func TestMachineConfig_MissingTemplateIs500(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.TalosConfigFile, "does-not-exist.yaml")
	seedApprovedHost(t, "aa:bb:cc:dd:ee:ff", hardware.Host{})

	req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac=aa:bb:cc:dd:ee:ff", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()
	store := machineConfigTestStore(t)

	handleMachineConfigRequest(store)(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// TestMachineConfig_RendersHostFields proves the host-field copy and the
// per-host schematic override: a registered host's hostname/IP render, and its
// schematic wins over the configured default.
func TestMachineConfig_RendersHostFields(t *testing.T) {
	dir := t.TempDir()
	tmpl := "machine:\n  hostname: {{ .Hostname }}\n  ip: {{ .IP }}\n  schematic: {{ .Schematic }}\n"
	if err := os.WriteFile(filepath.Join(dir, "machineconfig.yaml"), []byte(tmpl), 0o600); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")
	viper.Set(config.TalosConfigFile, "machineconfig.yaml")
	viper.Set(config.TalosSchematic, "default-schematic")

	// Load() resets the package-level HostDB against this fresh tempdir so the
	// seeded host is the only record; WriteMacAddress then registers + persists.
	if err := hardware.Load(); err != nil {
		t.Fatalf("hardware.Load: %v", err)
	}
	if err := hardware.WriteMacAddress("aa:bb:cc:dd:ee:ff", hardware.Host{
		Hostname:  "node-01",
		IP:        "10.0.0.50",
		Schematic: "host-schematic",
	}); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	approveHost(t, "aa:bb:cc:dd:ee:ff")

	req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac=aa:bb:cc:dd:ee:ff", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()
	store := machineConfigTestStore(t)

	handleMachineConfigRequest(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "node-01") {
		t.Errorf("Hostname not rendered: %q", body)
	}
	if !strings.Contains(body, "10.0.0.50") {
		t.Errorf("IP not rendered: %q", body)
	}
	if !strings.Contains(body, "host-schematic") {
		t.Errorf("host schematic override not rendered: %q", body)
	}
	if strings.Contains(body, "default-schematic") {
		t.Errorf("default schematic leaked despite host override: %q", body)
	}
}

// TestMachineConfig_ApprovedHostWithoutOverridesRendersDefault: an approved host
// with no hostname or schematic of its own gets the DEFAULT schematic and an
// empty hostname. (A caller with no approved host gets a 404 instead, #162; see
// TestMachineConfigDefaultRefusedWithoutApprovedHost.)
func TestMachineConfig_ApprovedHostWithoutOverridesRendersDefault(t *testing.T) {
	dir := t.TempDir()
	tmpl := "machine:\n  hostname: {{ .Hostname }}\n  schematic: {{ .Schematic }}\n"
	if err := os.WriteFile(filepath.Join(dir, "machineconfig.yaml"), []byte(tmpl), 0o600); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	viper.Set(config.TalosConfigFile, "machineconfig.yaml")
	viper.Set(config.TalosSchematic, "default-schematic")
	seedApprovedHost(t, "aa:bb:cc:dd:ee:ff", hardware.Host{})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/machineconfig?mac=aa:bb:cc:dd:ee:ff", http.NoBody)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()
	store := machineConfigTestStore(t)

	handleMachineConfigRequest(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "default-schematic") {
		t.Errorf("default schematic not rendered: %q", body)
	}
	if !strings.Contains(body, "hostname: \n") {
		t.Errorf("expected empty hostname for a host with none: %q", body)
	}
}

// TestMachineConfig_RegisteredBlankHostnameUsesHostnameQuery pins the guard:
// a registered host with an empty Hostname must not clobber the hostname query
// param, so the unnamed-but-known host still gets its query-supplied identity.
func TestMachineConfig_RegisteredBlankHostnameUsesHostnameQuery(t *testing.T) {
	dir := t.TempDir()
	tmpl := "machine:\n  hostname: {{ .Hostname }}\n"
	if err := os.WriteFile(filepath.Join(dir, "machineconfig.yaml"), []byte(tmpl), 0o600); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")
	viper.Set(config.TalosConfigFile, "machineconfig.yaml")
	viper.Set(config.TalosSchematic, "default-schematic")

	// Load() resets the package-level HostDB against this fresh tempdir so the
	// seeded host is the only record; WriteMacAddress registers it with a blank
	// Hostname so the DB record cannot supply one.
	if err := hardware.Load(); err != nil {
		t.Fatalf("hardware.Load: %v", err)
	}
	if err := hardware.WriteMacAddress("aa:bb:cc:dd:ee:ff", hardware.Host{
		Hostname: "",
		IP:       "10.0.0.50",
	}); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	approveHost(t, "aa:bb:cc:dd:ee:ff")

	req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac=aa:bb:cc:dd:ee:ff&hostname=node-x", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()
	store := machineConfigTestStore(t)

	handleMachineConfigRequest(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "node-x") {
		t.Errorf("blank-hostname host clobbered the query fallback: %q", rec.Body.String())
	}
}

// TestMachineConfig_ExecuteFailureIs500 covers the Execute branch of writeError:
// a template that parses but references a field the data struct lacks fails at
// Execute and must return 500.
func TestMachineConfig_ExecuteFailureIs500(t *testing.T) {
	dir := t.TempDir()
	tmpl := "machine:\n  bad: {{ .Nonexistent }}\n"
	if err := os.WriteFile(filepath.Join(dir, "machineconfig.yaml"), []byte(tmpl), 0o600); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	viper.Set(config.TalosConfigFile, "machineconfig.yaml")
	seedApprovedHost(t, "aa:bb:cc:dd:ee:ff", hardware.Host{})

	req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac=aa:bb:cc:dd:ee:ff", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()
	store := machineConfigTestStore(t)

	handleMachineConfigRequest(store)(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestMachineConfig_ValidatesHostnameQuery(t *testing.T) {
	dir := t.TempDir()
	tmpl := "machine:\n  install:\n    hostname: {{ .Hostname }}\n"
	if err := os.WriteFile(filepath.Join(dir, "machineconfig.yaml"), []byte(tmpl), 0o600); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	viper.Set(config.TalosConfigFile, "machineconfig.yaml")
	viper.Set(config.ServerIP, "10.0.0.1")
	viper.Set(config.ServerHttpPort, "80")
	seedApprovedHost(t, "aa:bb:cc:dd:ee:ff", hardware.Host{})
	store := machineConfigTestStore(t)

	for _, tc := range []struct {
		query    string
		want     int
		contains string
	}{
		// An invalid ?hostname= comes from DHCP: it is ignored, not a 400.
		{"mac=aa:bb:cc:dd:ee:ff&hostname=bad_name", http.StatusOK, "hostname: \n"},
		{"mac=aa:bb:cc:dd:ee:ff&hostname=", http.StatusOK, ""},
		{"mac=aa:bb:cc:dd:ee:ff&hostname=node-x", http.StatusOK, "node-x"},
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/machineconfig?"+tc.query, http.NoBody)
		req.RemoteAddr = "192.0.2.1:12345"
		rec := httptest.NewRecorder()
		machineConfigHandler(store, failingARP)(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d; body=%q", tc.query, rec.Code, tc.want, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.contains) {
			t.Errorf("%s: body %q lacks %q", tc.query, rec.Body.String(), tc.contains)
		}
		if strings.Contains(rec.Body.String(), "bad_name") {
			t.Errorf("%s: invalid hostname reached the template: %q", tc.query, rec.Body.String())
		}
	}
}

// TestMachineConfig_InvalidHostnameQueryIgnoredForKnownHost: a known host's
// stored hostname still wins, and an invalid query hostname never renders,
// whether or not the host has a stored hostname.
func TestMachineConfig_InvalidHostnameQueryIgnoredForKnownHost(t *testing.T) {
	dir := t.TempDir()
	tmpl := "machine:\n  hostname: {{ .Hostname }}\n"
	if err := os.WriteFile(filepath.Join(dir, "machineconfig.yaml"), []byte(tmpl), 0o600); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")
	viper.Set(config.TalosConfigFile, "machineconfig.yaml")
	if err := hardware.Load(); err != nil {
		t.Fatalf("hardware.Load: %v", err)
	}
	if err := hardware.WriteMacAddress("aa:bb:cc:dd:ee:01", hardware.Host{Hostname: "stored-01"}); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	if err := hardware.WriteMacAddress("aa:bb:cc:dd:ee:02", hardware.Host{}); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	approveHost(t, "aa:bb:cc:dd:ee:01")
	approveHost(t, "aa:bb:cc:dd:ee:02")
	store := machineConfigTestStore(t)

	for _, tc := range []struct {
		mac, contains string
	}{
		{"aa:bb:cc:dd:ee:01", "hostname: stored-01\n"},
		{"aa:bb:cc:dd:ee:02", "hostname: \n"},
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/machineconfig?mac="+tc.mac+"&hostname=bad_name", http.NoBody)
		req.RemoteAddr = "192.0.2.1:12345"
		rec := httptest.NewRecorder()
		machineConfigHandler(store, failingARP)(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200; body=%q", tc.mac, rec.Code, rec.Body.String())
			continue
		}
		if body := rec.Body.String(); !strings.Contains(body, tc.contains) || strings.Contains(body, "bad_name") {
			t.Errorf("%s: body %q, want %q and no bad_name", tc.mac, body, tc.contains)
		}
	}
}
