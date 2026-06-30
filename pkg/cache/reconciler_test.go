package cache

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// TestReconciler_StartRunsStartupReconcileThenStop verifies Start fires an
// immediate reconcile over all targets and Stop terminates the coordinator.
// The test is fully hermetic: ALL upstreams (talos factory, flatcar, coreos
// streams + builds) point at one httptest server, so no real network call is
// made even though seedTargets also seeds the flatcar/fedora-coreos predefined
// targets. The talos path returns a real version list + artifact bytes; the
// flatcar/coreos paths return a benign 404, and those discovery failures are
// non-fatal slog.Warns — the test still asserts the talos target caches.
func TestReconciler_StartRunsStartupReconcileThenStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/versions": // talos factory discovery
			_, _ = w.Write([]byte(`["v1.10.5"]`))
		case strings.Contains(r.URL.Path, "/image/"): // talos artifacts
			_, _ = w.Write([]byte("artifact-bytes"))
		default: // flatcar version.txt + coreos streams/builds: benign miss
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	// Redirect every discovery/artifact upstream at the one server (hermetic).
	viper.Set(config.TalosFactoryURL, srv.URL)
	viper.Set(config.FlatcarURL, srv.URL+"/%s/%s")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.CoreOSStreamsURL, srv.URL+"/streams/%s.json")
	viper.Set(config.CoreOSURL, srv.URL+"/%s/%s/%s")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosSchematic, "defschem")
	viper.Set(config.TalosRetainMinors, 1)

	store, err := db.Open(filepath.Join(t.TempDir(), "booty.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.CreateTarget(db.Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"s"}`, Mode: "discovery", RetainN: 1, Predefined: true, Enabled: true}); err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}

	r := NewReconciler(store, time.Hour, 4) // long interval: rely on the startup reconcile
	r.Start(t.Context())

	// Poll for the startup reconcile to land the version on disk.
	deadline := time.Now().Add(5 * time.Second)
	for NewestCached("talos", "amd64", map[string]string{"schematic": "s"}) == "" {
		if time.Now().After(deadline) {
			t.Fatal("startup reconcile did not cache v1.10.5 within 5s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.Stop() // must return promptly and not panic / double-close
}

func TestTriggerCoalesces(t *testing.T) {
	r := NewReconciler(nil, time.Hour, 1)
	// Trigger before Start: fills the buffered channel without blocking, and a
	// second call coalesces (no panic, no block).
	r.Trigger()
	r.Trigger()
	if len(r.trigger) != 1 {
		t.Fatalf("trigger depth = %d, want 1 (coalesced)", len(r.trigger))
	}
}
