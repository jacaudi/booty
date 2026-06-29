package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func seedCache(t *testing.T, cacheName, segment, arch, version string) {
	t.Helper()
	dir := filepath.Join(viper.GetString(config.DataDir), "cache", cacheName, segment, arch, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
}

func TestHandleVersionRequest_SourcedFromCache(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	seedCache(t, "flatcar", "-", "amd64", "3815.2.0")
	seedCache(t, "coreos", "-", "x86_64", "39.20231101.3.0")

	// text form
	rr := httptest.NewRecorder()
	handleVersionRequest(rr, httptest.NewRequest(http.MethodGet, "/version.txt", nil))
	body := rr.Body.String()
	if !strings.Contains(body, "FLATCAR_VERSION=3815.2.0") || !strings.Contains(body, "COREOS_VERSION=39.20231101.3.0") {
		t.Errorf("/version.txt body = %q", body)
	}

	// json form
	rr = httptest.NewRecorder()
	handleVersionRequest(rr, httptest.NewRequest(http.MethodGet, "/version.json", nil))
	jb := rr.Body.String()
	if !strings.Contains(jb, `"flatcar":"3815.2.0"`) || !strings.Contains(jb, `"coreos":"39.20231101.3.0"`) {
		t.Errorf("/version.json body = %q", jb)
	}
}

func TestHandleInfoRequest_SourcedFromCache(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	seedCache(t, "flatcar", "-", "amd64", "3815.2.0")

	rr := httptest.NewRecorder()
	handleInfoRequest(rr, httptest.NewRequest(http.MethodGet, "/info", nil))
	if !strings.Contains(rr.Body.String(), `"flatcar":{"version":"3815.2.0"}`) {
		t.Errorf("/info body = %q", rr.Body.String())
	}
}
