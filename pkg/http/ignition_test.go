package http

import (
	"bytes"
	"log/slog"
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

// TestHandleIgnitionRequest_EmptyMACServesRebootConfigQuietly verifies that an
// unidentified-host boot (no ?mac= and ARP yields nothing) is treated as the
// expected "miss": the handler must NOT emit the "error looking up host" Warn
// and must serve the reboot config (the host==nil path).
func TestHandleIgnitionRequest_EmptyMACServesRebootConfigQuietly(t *testing.T) {
	dir := t.TempDir()
	// host==nil path still parses the ignition template, so it must exist.
	if err := os.WriteFile(filepath.Join(dir, "ignition.yaml"), []byte("variant: fcos\nversion: 1.5.0\n"), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	viper.Set(config.IgnitionFile, "ignition.yaml")
	viper.Set(config.ServerIP, "127.0.0.1")
	viper.Set(config.ServerHttpPort, "80")

	var logBuf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })

	req := httptest.NewRequest(http.MethodGet, "/ignition", nil)
	req.RemoteAddr = "192.0.2.1:12345" // TEST-NET-1; ARP will not resolve
	rec := httptest.NewRecorder()

	handleIgnitionRequest(rec, req)

	if strings.Contains(logBuf.String(), "error looking up host") {
		t.Errorf("unidentified-host boot logged a host-lookup error; want it quiet.\nlogs:\n%s", logBuf.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "reboot") {
		t.Errorf("empty-MAC request did not serve the reboot config; body = %q", body)
	}
}

func TestHandleIgnitionRequest_BadTemplateReturns500(t *testing.T) {
	dir := t.TempDir()
	// A registered host forces the Butane translate path; give it a template
	// that renders (valid Go text/template) but is invalid Butane → fatal.
	if err := os.WriteFile(filepath.Join(dir, "ignition.yaml"), []byte("not: valid: butane: at all\n"), 0o600); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hardware.json"),
		[]byte(`{"aa:bb:cc:dd:ee:ff":{"mac":"aa:bb:cc:dd:ee:ff","hostname":"n1"}}`), 0o600); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")
	viper.Set(config.IgnitionFile, "ignition.yaml")
	viper.Set(config.ServerIP, "127.0.0.1")
	viper.Set(config.ServerHttpPort, "80")
	if err := hardware.Load(); err != nil {
		t.Fatalf("hardware.Load: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/ignition.json?mac=aa:bb:cc:dd:ee:ff", nil)
	rec := httptest.NewRecorder()
	handleIgnitionRequest(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for a genuine ignition failure", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "ignition") {
		t.Errorf("body should carry a short ignition error message, got %q", body)
	}
	if strings.Contains(body, "mapping values") {
		t.Errorf("raw Butane/yaml error leaked to the wire: %q", body)
	}
}
