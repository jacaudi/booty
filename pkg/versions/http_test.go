package versions

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

// TestFetchVersionMetadata_TimesOut verifies that a slow upstream does not
// hang the version check: with the timeout shrunk below the server's response
// delay, the request is cancelled and an error is returned instead of blocking.
func TestFetchVersionMetadata_TimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte("too late"))
	}))
	t.Cleanup(srv.Close)

	orig := versionCheckTimeout
	versionCheckTimeout = 20 * time.Millisecond
	t.Cleanup(func() { versionCheckTimeout = orig })

	start := time.Now()
	_, err := fetchVersionMetadata(srv.URL)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("fetchVersionMetadata: err = nil, want timeout error")
	}
	if elapsed >= 200*time.Millisecond {
		t.Errorf("fetchVersionMetadata blocked for %v, want it to abort near the %v timeout", elapsed, versionCheckTimeout)
	}
}

// TestFetchVersionMetadata_ReturnsBody verifies a normal response is read and
// returned intact.
func TestFetchVersionMetadata_ReturnsBody(t *testing.T) {
	want := "FLATCAR_VERSION=1.2.3\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(want))
	}))
	t.Cleanup(srv.Close)

	b, err := fetchVersionMetadata(srv.URL)
	if err != nil {
		t.Fatalf("fetchVersionMetadata: %v", err)
	}
	if string(b) != want {
		t.Errorf("fetchVersionMetadata body = %q, want %q", b, want)
	}
}

// TestFetchVersionMetadata_Non2xxIsError verifies an error status surfaces as
// an error rather than a body to parse.
func TestFetchVersionMetadata_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	if _, err := fetchVersionMetadata(srv.URL); err == nil {
		t.Fatalf("fetchVersionMetadata: err = nil, want error for 404")
	}
}

// TestLoadRemoteFlatcarVersion_TimesOut verifies the end-to-end flatcar check
// does not set a remote version when the upstream is too slow.
func TestLoadRemoteFlatcarVersion_TimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte("FLATCAR_VERSION=9.9.9\n"))
	}))
	t.Cleanup(srv.Close)

	orig := versionCheckTimeout
	versionCheckTimeout = 20 * time.Millisecond
	t.Cleanup(func() { versionCheckTimeout = orig })

	viper.Reset()
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.FlatcarURL, srv.URL+"/%s-%s")

	LoadRemoteFlatcarVersion()

	if got := viper.GetString(config.RemoteFlatcarVersion); got != "" {
		t.Errorf("RemoteFlatcarVersion = %q, want empty after timeout", got)
	}
}

// TestLoadRemoteFlatcarVersion_ParsesNormalResponse verifies a healthy
// upstream sets the parsed remote version.
func TestLoadRemoteFlatcarVersion_ParsesNormalResponse(t *testing.T) {
	srv, _ := counterServer(t, "FLATCAR_VERSION=4.5.6")
	viper.Reset()
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.FlatcarURL, srv.URL+"/%s-%s")

	LoadRemoteFlatcarVersion()

	if got := viper.GetString(config.RemoteFlatcarVersion); got != "4.5.6" {
		t.Errorf("RemoteFlatcarVersion = %q, want %q", got, "4.5.6")
	}
}

// sanity: timeout default is the documented short value, not the 5m download ceiling.
func TestVersionCheckTimeoutDefault(t *testing.T) {
	if versionCheckTimeout != 30*time.Second {
		t.Errorf("versionCheckTimeout = %v, want 30s", versionCheckTimeout)
	}
}
