package cache

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// TestReconcileTarget_TalosCachesRetainedAndArchived drives a fake Talos factory:
// discovery returns three minor lines; retain_n=2 keeps the newest two and the
// reconciler downloads their artifacts and records cached=1; a stale discovered
// row is ARCHIVED (in_window=0) and its dir is kept on disk for rollback/boot.
func TestReconcileTarget_TalosCachesRetainedAndArchived(t *testing.T) {
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
	// Stale row is archived (kept in DB) and its dir is kept on disk for rollback/boot.
	// v1.7.0 was seeded without a cache_entries row, so SetCacheInWindow is a no-op
	// on it; no in_window assertion is needed here — archive→in_window=0 is covered
	// by TestReconcileArchivesRotatedOut.
	if _, ok := cached["v1.7.0"]; !ok {
		t.Errorf("archived v1.7.0 row was deleted from DB: %v", cached)
	}
	if _, err := os.Stat(cacheDir("talos", "schem1", "amd64", "v1.7.0")); err != nil {
		t.Errorf("archived v1.7.0 dir was removed from disk: stat err = %v", err)
	}
}

// newCacheEntryFixture spins a fake Talos factory whose /versions response can be
// changed between reconciles (via the returned *string), plus a real talos target.
// reconcileTarget is synchronous, so writes to *versions never overlap an in-flight
// request (no race).
func newCacheEntryFixture(t *testing.T, retainN int) (*db.Store, int64, *string) {
	t.Helper()
	versions := new(string)
	*versions = `[]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/versions" {
			_, _ = w.Write([]byte(*versions))
			return
		}
		_, _ = w.Write([]byte("artifact-bytes")) // any /image/<schematic>/<version>/<file>
	}))
	t.Cleanup(srv.Close)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.TalosFactoryURL, srv.URL)
	store := newReconcileStore(t)
	tid, err := store.CreateTarget(db.Target{
		OS: "talos", Arch: "amd64", Params: `{"schematic":"schem1"}`,
		Mode: "discovery", RetainN: retainN, Predefined: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	return store, tid, versions
}

func TestReconcileWritesCacheEntry(t *testing.T) {
	store, tid, versions := newCacheEntryFixture(t, 3)
	*versions = `["v1.13.5"]`
	tgt, _ := store.GetTarget(tid)
	if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
		t.Fatal(err)
	}
	rows, _ := store.ListCacheEntries(db.CacheFilter{})
	if len(rows) != 1 || rows[0].Version != "v1.13.5" || !rows[0].InWindow || rows[0].Size <= 0 {
		t.Fatalf("want one in-window cache_entry with size>0, got %+v", rows)
	}
}

func TestReconcileArchivesRotatedOut(t *testing.T) {
	store, tid, versions := newCacheEntryFixture(t, 1) // retain_n=1 (newest minor line)
	*versions = `["v1.12.9"]`
	tgt, _ := store.GetTarget(tid)
	if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
		t.Fatal(err)
	}
	*versions = `["v1.12.9","v1.13.5"]` // newer minor line; retain_n=1 keeps 1.13, archives 1.12
	if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
		t.Fatal(err)
	}
	rows, _ := store.ListCacheEntries(db.CacheFilter{})
	byVer := map[string]db.CacheEntryRow{}
	for _, r := range rows {
		byVer[r.Version] = r
	}
	if _, ok := byVer["v1.12.9"]; !ok {
		t.Fatal("v1.12.9 must NOT be deleted; it should be archived")
	}
	if byVer["v1.12.9"].InWindow {
		t.Fatal("v1.12.9 should be archived (in_window=0)")
	}
	if !byVer["v1.13.5"].InWindow {
		t.Fatal("v1.13.5 should be in-window")
	}
	if !cacheDirExists("talos", "schem1", "amd64", "v1.12.9") {
		t.Fatal("v1.12.9 dir must remain on disk after archiving")
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

// newFlatcarFixture spins a fake Flatcar release server whose version.txt
// response can be changed between reconciles (via the returned *string), plus
// a channel-params flatcar target. reconcileTarget is synchronous, so writes
// to *version never overlap an in-flight request.
func newFlatcarFixture(t *testing.T, retainN int) (*db.Store, int64, *string) {
	t.Helper()
	version := new(string)
	*version = "100.0.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/version.txt") {
			_, _ = w.Write([]byte("FLATCAR_VERSION=" + *version + "\n"))
			return
		}
		_, _ = w.Write([]byte("artifact-bytes")) // vmlinuz / cpio.gz
	}))
	t.Cleanup(srv.Close)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.FlatcarURL, srv.URL+"/%s/%s")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.FlatcarArchitecture, "amd64")
	store := newReconcileStore(t)
	tid, err := store.CreateTarget(db.Target{
		OS: "flatcar", Arch: "amd64", Params: `{"channel":"stable"}`,
		Mode: "discovery", RetainN: retainN, Predefined: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	return store, tid, version
}

// TestReconcileFlatcarAccumulatesWindow: with retainN=2, a single-version-
// discovery OS keeps the previous release in-window when upstream moves on —
// the #48 retention union (issue acceptance criterion 4).
func TestReconcileFlatcarAccumulatesWindow(t *testing.T) {
	store, tid, version := newFlatcarFixture(t, 2)
	tgt, _ := store.GetTarget(tid)
	if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
		t.Fatal(err)
	}
	*version = "100.1.0" // upstream releases; 100.0.0 no longer advertised
	if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
		t.Fatal(err)
	}
	inWin, err := store.ListCachedInWindowVersions(tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(inWin) != 2 {
		t.Fatalf("retainN=2 must keep both releases in-window, got %v", inWin)
	}
	if !cacheDirExists("flatcar", "stable", "amd64", "100.0.0") {
		t.Fatal("previous release's artifacts must remain under the channel segment")
	}
}

// TestReconcileFlatcarRetainOneArchivesAndNeverResurrects: with retainN=1 the
// old release is archived when upstream moves on, and — because the union
// draws only from in-window AND cached — it stays archived on every later tick.
func TestReconcileFlatcarRetainOneArchivesAndNeverResurrects(t *testing.T) {
	store, tid, version := newFlatcarFixture(t, 1)
	tgt, _ := store.GetTarget(tid)
	if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
		t.Fatal(err)
	}
	*version = "100.1.0"
	for range 3 { // several ticks: archived must not resurrect
		if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
			t.Fatal(err)
		}
	}
	inWin, _ := store.ListCachedInWindowVersions(tid)
	if len(inWin) != 1 || inWin[0] != "100.1.0" {
		t.Fatalf("retainN=1: only the newest may be in-window, got %v", inWin)
	}
	rows, _ := store.ListCacheEntries(db.CacheFilter{})
	for _, r := range rows {
		if r.Version == "100.0.0" && r.InWindow {
			t.Fatal("archived 100.0.0 resurrected into the window (union must exclude archived)")
		}
	}
}
