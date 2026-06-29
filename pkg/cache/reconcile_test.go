package cache

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

func newReconcileStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.Open(filepath.Join(t.TempDir(), "booty.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestReconcileTarget_TalosCachesRetainedAndPrunes drives a fake Talos factory:
// discovery returns three minor lines; retain_n=2 keeps the newest two and the
// reconciler downloads their artifacts and records cached=1; a stale discovered
// row is pruned (DB row + dir).
func TestReconcileTarget_TalosCachesRetainedAndPrunes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/versions":
			_, _ = w.Write([]byte(`["v1.8.0","v1.9.0","v1.10.5"]`))
		default: // any /image/<schematic>/<version>/<file>
			_, _ = w.Write([]byte("artifact-bytes"))
		}
	}))
	t.Cleanup(srv.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.TalosFactoryURL, srv.URL)

	store := newReconcileStore(t)
	tid, err := store.CreateTarget(db.Target{
		OS: "talos", Arch: "amd64", Params: `{"schematic":"schem1"}`,
		Mode: "discovery", RetainN: 2, Predefined: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	// Pre-seed a stale discovered version (older minor line) to be pruned.
	if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: tid, Version: "v1.7.0", Source: "discovered", Cached: true}); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	if err := os.MkdirAll(cacheDir("talos", "schem1", "amd64", "v1.7.0"), 0o755); err != nil {
		t.Fatalf("seed stale dir: %v", err)
	}

	tgt, _ := store.GetTarget(tid)
	if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
		t.Fatalf("reconcileTarget: %v", err)
	}

	// Newest two minor lines cached on disk + flagged cached=1.
	if got := NewestCached("talos", "amd64", map[string]string{"schematic": "schem1"}); got != "v1.10.5" {
		t.Errorf("NewestCached = %q, want v1.10.5", got)
	}
	versionsRows, _ := store.ListTargetVersions(tid)
	cached := map[string]bool{}
	for _, v := range versionsRows {
		cached[v.Version] = v.Cached
	}
	if !cached["v1.10.5"] || !cached["v1.9.0"] {
		t.Errorf("expected v1.10.5 and v1.9.0 cached, got %v", cached)
	}
	// Stale row + dir pruned.
	if _, ok := cached["v1.7.0"]; ok {
		t.Errorf("stale v1.7.0 row not pruned: %v", cached)
	}
	if _, err := os.Stat(cacheDir("talos", "schem1", "amd64", "v1.7.0")); !os.IsNotExist(err) {
		t.Errorf("stale dir not removed: stat err = %v", err)
	}
}

// TestReconcileTarget_ManualNeverPruned: a manual pin survives a reconcile even
// when discovery does not include it.
func TestReconcileTarget_ManualNeverPruned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/versions" {
			_, _ = w.Write([]byte(`["v1.10.5"]`))
			return
		}
		_, _ = w.Write([]byte("x"))
	}))
	t.Cleanup(srv.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.TalosFactoryURL, srv.URL)

	store := newReconcileStore(t)
	tid, _ := store.CreateTarget(db.Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"s"}`, Mode: "discovery", RetainN: 1, Enabled: true})
	if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: tid, Version: "v1.5.0", Source: "manual", Cached: true}); err != nil {
		t.Fatalf("seed manual: %v", err)
	}

	tgt, _ := store.GetTarget(tid)
	if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
		t.Fatalf("reconcileTarget: %v", err)
	}

	rows, _ := store.ListTargetVersions(tid)
	var sawManual bool
	for _, v := range rows {
		if v.Version == "v1.5.0" && v.Source == "manual" {
			sawManual = true
		}
	}
	if !sawManual {
		t.Errorf("manual pin v1.5.0 was pruned: %v", rows)
	}
}
