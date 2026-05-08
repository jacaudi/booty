package versions

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

// counterServer returns an httptest.Server that increments hits per request
// and serves the given version.txt body for any path ending in /version.txt.
func counterServer(t *testing.T, versionTxtBody string) (*httptest.Server, *atomic.Int64) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if strings.HasSuffix(r.URL.Path, "/version.txt") {
			fmt.Fprintln(w, versionTxtBody)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestFlatcarVersionCheck_EarlyReturnWhenOwnFlagSet verifies that when
// UpdatingFlatcar is true, FlatcarVersionCheck returns immediately without
// making any HTTP calls.
func TestFlatcarVersionCheck_EarlyReturnWhenOwnFlagSet(t *testing.T) {
	viper.Reset()
	srv, hits := counterServer(t, "FLATCAR_VERSION=1.0.0")

	viper.Set(config.UpdatingFlatcar, true)
	viper.Set(config.UpdatingCoreOS, false)
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.FlatcarURL, srv.URL+"/%s-%s")
	viper.Set(config.CurrentFlatcarVersion, "1.0.0")

	FlatcarVersionCheck()

	if got := hits.Load(); got != 0 {
		t.Errorf("expected 0 HTTP hits when UpdatingFlatcar=true, got %d", got)
	}
}

// TestFlatcarVersionCheck_RunsWhenOnlyCoreOSUpdating verifies that when
// only CoreOS is updating, FlatcarVersionCheck still runs (proving the
// flags are independent).
func TestFlatcarVersionCheck_RunsWhenOnlyCoreOSUpdating(t *testing.T) {
	viper.Reset()
	srv, hits := counterServer(t, "FLATCAR_VERSION=1.0.0")

	viper.Set(config.UpdatingFlatcar, false)
	viper.Set(config.UpdatingCoreOS, true)
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.FlatcarURL, srv.URL+"/%s-%s")
	// Pre-set CurrentFlatcarVersion so the seeding path is skipped, and the
	// remote will return the same value so the update branch is also skipped.
	viper.Set(config.CurrentFlatcarVersion, "1.0.0")

	FlatcarVersionCheck()

	if got := hits.Load(); got == 0 {
		t.Errorf("expected FlatcarVersionCheck to make HTTP calls when only CoreOS is updating, got 0 hits")
	}
}
