package http

import (
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

	req := httptest.NewRequest(http.MethodGet, "/machineconfig?serial=ABC123&mac=aa:bb:cc:dd:ee:ff", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()

	handleMachineConfigRequest(rec, req)

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

	req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac=aa:bb:cc:dd:ee:ff", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()

	handleMachineConfigRequest(rec, req)

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

	req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac=aa:bb:cc:dd:ee:ff", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()

	handleMachineConfigRequest(rec, req)

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

// TestMachineConfig_UnidentifiedHostRendersDefault pins the deliberate Talos
// behavior: an unidentified host (no ?mac=, ARP can't resolve) gets a host-less
// config with the DEFAULT schematic, not ignition's reboot-on-unknown.
func TestMachineConfig_UnidentifiedHostRendersDefault(t *testing.T) {
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

	// No ?mac=; RemoteAddr is RFC 5737 TEST-NET-1, so ARP cannot resolve a MAC
	// and the host stays nil.
	req := httptest.NewRequest(http.MethodGet, "/machineconfig", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()

	handleMachineConfigRequest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "default-schematic") {
		t.Errorf("default schematic not rendered: %q", body)
	}
	if !strings.Contains(body, "hostname: \n") {
		t.Errorf("expected empty hostname for unidentified host: %q", body)
	}
}

// TestMachineConfig_UnidentifiedHostUsesHostnameQuery proves the hostname query
// param is the fallback identity source for the unregistered first boot.
func TestMachineConfig_UnidentifiedHostUsesHostnameQuery(t *testing.T) {
	dir := t.TempDir()
	tmpl := "machine:\n  hostname: {{ .Hostname }}\n"
	if err := os.WriteFile(filepath.Join(dir, "machineconfig.yaml"), []byte(tmpl), 0o600); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	viper.Set(config.TalosConfigFile, "machineconfig.yaml")
	viper.Set(config.TalosSchematic, "default-schematic")

	req := httptest.NewRequest(http.MethodGet, "/machineconfig?hostname=node-x", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()

	handleMachineConfigRequest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "node-x") {
		t.Errorf("hostname query not rendered: %q", rec.Body.String())
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

	req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac=aa:bb:cc:dd:ee:ff", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()

	handleMachineConfigRequest(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
