package cache

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/ostype"
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
		Mode: "discovery", RetainN: 2, Source: "catalog", Enabled: true,
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
		Mode: "discovery", RetainN: retainN, Source: "catalog", Enabled: true,
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
	tid, _ := store.CreateTarget(db.Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"s"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
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
	// These tests exercise retention-window behavior, not signatures. The fixture
	// serves fake ".sig" bytes that cannot be a valid signature by the embedded
	// PRODUCTION flatcar key, so verification would classify them as a forgery and
	// reject. Run under `off` so land is signature-independent; the admission gate
	// is covered separately (TestLandArtifact_PolicyTable + TestReconcileFCOSVerification).
	viper.Set(config.SignaturePolicy, "off")
	store := newReconcileStore(t)
	tid, err := store.CreateTarget(db.Target{
		OS: "flatcar", Arch: "amd64", Params: `{"channel":"stable"}`,
		Mode: "discovery", RetainN: retainN, Source: "catalog", Enabled: true,
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

// TestReconcileTwoFlatcarChannelsCacheIndependently is the issue #48 acceptance
// criterion 3 e2e test: two targets for the same OS/arch but different channel
// params must cache under distinct channel segments and never cross-pollute
// each other's retention window.
func TestReconcileTwoFlatcarChannelsCacheIndependently(t *testing.T) {
	versions := map[string]string{"stable": "100.1.0", "beta": "200.1.0"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var channel string
		switch {
		case strings.HasPrefix(r.URL.Path, "/stable/"):
			channel = "stable"
		case strings.HasPrefix(r.URL.Path, "/beta/"):
			channel = "beta"
		}
		if strings.HasSuffix(r.URL.Path, "/version.txt") {
			_, _ = w.Write([]byte("FLATCAR_VERSION=" + versions[channel] + "\n"))
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
	// Retention/independence test — land signature-independently (see newFlatcarFixture).
	viper.Set(config.SignaturePolicy, "off")
	store := newReconcileStore(t)

	stableID, err := store.CreateTarget(db.Target{
		OS: "flatcar", Arch: "amd64", Params: `{"channel":"stable"}`,
		Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateTarget stable: %v", err)
	}
	betaID, err := store.CreateTarget(db.Target{
		OS: "flatcar", Arch: "amd64", Params: `{"channel":"beta"}`,
		Mode: "discovery", RetainN: 1, Source: "api", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateTarget beta: %v", err)
	}

	stableTgt, _ := store.GetTarget(stableID)
	if err := reconcileTarget(t.Context(), store, 4, *stableTgt); err != nil {
		t.Fatalf("reconcile stable: %v", err)
	}
	betaTgt, _ := store.GetTarget(betaID)
	if err := reconcileTarget(t.Context(), store, 4, *betaTgt); err != nil {
		t.Fatalf("reconcile beta: %v", err)
	}

	if !cacheDirExists("flatcar", "stable", "amd64", "100.1.0") {
		t.Error("stable channel must cache under its own channel segment")
	}
	if !cacheDirExists("flatcar", "beta", "amd64", "200.1.0") {
		t.Error("beta channel must cache under its own channel segment")
	}

	stableWin, err := store.ListCachedInWindowVersions(stableID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stableWin) != 1 || stableWin[0] != "100.1.0" {
		t.Errorf("stable target's in-window versions = %v, want [100.1.0] only", stableWin)
	}
	betaWin, err := store.ListCachedInWindowVersions(betaID)
	if err != nil {
		t.Fatal(err)
	}
	if len(betaWin) != 1 || betaWin[0] != "200.1.0" {
		t.Errorf("beta target's in-window versions = %v, want [200.1.0] only", betaWin)
	}
}

// TestReconcileManualPinDoesNotDisplaceDiscoveredFromWindow: a manual pin must
// not consume a retention-window slot — it is always desired and never
// archived by the prune loop, so it must not displace the discovered current
// version out of the in-window set (issue final-review item 2).
func TestReconcileManualPinDoesNotDisplaceDiscoveredFromWindow(t *testing.T) {
	store, tid, _ := newFlatcarFixture(t, 1)
	tgt, _ := store.GetTarget(tid)
	if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: tid, Version: "200.0.0", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	tgt, _ = store.GetTarget(tid)
	if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
		t.Fatal(err)
	}

	inWin, err := store.ListCachedInWindowVersions(tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(inWin) != 1 || inWin[0] != "100.0.0" {
		t.Fatalf("discovered current must stay in-window despite the manual pin, got %v", inWin)
	}

	rows, _ := store.ListTargetVersions(tid)
	var manualCached bool
	for _, r := range rows {
		if r.Version == "200.0.0" && r.Source == "manual" && r.Cached {
			manualCached = true
		}
	}
	if !manualCached {
		t.Fatal("manual pin 200.0.0 must still be cached")
	}
}

// TestLandArtifact_PolicyTable drives landArtifact directly with hand-built
// Artifacts (the unit under test), covering the §5 D15 matrix without needing a
// full reconcile. Exactly the rows below: a matching sha256 lands; a checksum
// mismatch (corruption) lands under warn and is rejected under strict; a
// signature mismatch (FORGERY) is rejected under BOTH warn AND strict — the
// key admission property, that warn refuses forgeries; a valid GPG signature
// lands; and under off nothing is verified (lands, not-verifiable). This is the
// ONLY place the forgery-rejected-under-warn branch is exercised end-to-end
// (reconcile cannot reach it — see Step-1 mechanism note below).
func TestLandArtifact_PolicyTable(t *testing.T) {
	body := []byte("artifact-bytes")
	sum := hexSHA(body)
	keyring, sigURL, closeGood := gpgFixture(t, body) // valid sig over body
	t.Cleanup(closeGood)
	// Forgery: a signature made over DIFFERENT bytes ("other") by a key that IS
	// in forgeKeyring, then checked against the served body → the key is known
	// but the content does not match → RSA verification failure → classForgery
	// (NOT ErrUnknownIssuer, which would be corruption). This is what "forgery"
	// means: a valid-by-a-trusted-key signature over content that was tampered.
	forgeKeyring, forgeSigURL, closeForge := gpgFixture(t, []byte("other"))
	t.Cleanup(closeForge)

	art := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(art.Close)
	base := art.URL

	cases := []struct {
		name       string
		policy     string
		a          ostype.Artifact
		wantLanded bool
		wantClass  verifyClass
	}{
		{"pass sha256 warn", "warn", ostype.Artifact{Filename: "rootfs.img", URL: base + "/rootfs.img", SHA256: sum}, true, classPass},
		{"checksum mismatch warn lands", "warn", ostype.Artifact{Filename: "rootfs.img", URL: base + "/rootfs.img", SHA256: hexSHA([]byte("x"))}, true, classCorruption},
		{"checksum mismatch strict rejects", "strict", ostype.Artifact{Filename: "rootfs.img", URL: base + "/rootfs.img", SHA256: hexSHA([]byte("x"))}, false, classCorruption},
		{"valid sig warn lands", "warn", ostype.Artifact{Filename: "vmlinuz", URL: base + "/vmlinuz", SigURL: sigURL, GPGKey: keyring}, true, classPass},
		// FORGERY rejects under BOTH policies — warn is NOT "provenance advisory".
		{"forgery warn rejects", "warn", ostype.Artifact{Filename: "vmlinuz", URL: base + "/vmlinuz", SigURL: forgeSigURL, GPGKey: forgeKeyring}, false, classForgery},
		{"forgery strict rejects", "strict", ostype.Artifact{Filename: "vmlinuz", URL: base + "/vmlinuz", SigURL: forgeSigURL, GPGKey: forgeKeyring}, false, classForgery},
		{"off never verifies", "off", ostype.Artifact{Filename: "rootfs.img", URL: base + "/rootfs.img", SHA256: hexSHA([]byte("x"))}, true, classNotVerifiable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			landed, v, err := landArtifact(t.Context(), dir, tc.a, tc.policy)
			if err != nil {
				t.Fatalf("landArtifact: %v", err)
			}
			if landed != tc.wantLanded || v.class != tc.wantClass {
				t.Fatalf("landed=%v class=%d, want landed=%v class=%d", landed, v.class, tc.wantLanded, tc.wantClass)
			}
			final := filepath.Join(dir, filepath.Base(tc.a.Filename))
			_, statErr := os.Stat(final)
			if tc.wantLanded && statErr != nil {
				t.Errorf("landed artifact must exist at final path")
			}
			if !tc.wantLanded && statErr == nil {
				t.Errorf("rejected artifact must NOT exist at final path")
			}
			if _, err := os.Stat(final + ".partial"); err == nil {
				t.Errorf(".partial must never remain after landArtifact")
			}
		})
	}
}

// TestReconcileSkipsAlreadyCachedVersion proves the land-path idempotency the
// retired ensureArtifact used to give: once a version is fully cached (bytes on
// disk + cached=1), a SECOND reconcile tick must NOT re-download its artifacts.
// A per-artifact request counter (non-.json = artifact byte fetch) makes the
// regression observable: without the version-level skip guard the second tick
// re-runs DownloadStaged for every artifact and the counter climbs; with it the
// counter stays put and the version remains cached=1. Runs under the default
// `warn` with matching sha256 so the version lands verified=1 (cached+verified).
func TestReconcileSkipsAlreadyCachedVersion(t *testing.T) {
	body := []byte("fcos-artifact")
	sha := hexSHA(body)
	var artifactHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".json") {
			_, _ = fmt.Fprintf(w, `{"architectures":{"x86_64":{"artifacts":{"metal":{`+
				`"release":"44.0.0.0","formats":{"pxe":{`+
				`"kernel":{"location":"%[1]s/44/kernel","sha256":"%[2]s"},`+
				`"initramfs":{"location":"%[1]s/44/initramfs","sha256":"%[2]s"},`+
				`"rootfs":{"location":"%[1]s/44/rootfs","sha256":"%[2]s"}`+
				`}}}}}}}`, "http://"+r.Host, sha)
			return
		}
		artifactHits.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.CoreOSStreamsURL, srv.URL+"/%s.json")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.SignaturePolicy, "warn")

	store := newReconcileStore(t)
	tid, err := store.CreateTarget(db.Target{OS: "fedora-coreos", Arch: "x86_64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	tgt, _ := store.GetTarget(tid)

	// First tick: artifacts are fetched and the version lands cached+verified.
	if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
		t.Fatal(err)
	}
	firstHits := artifactHits.Load()
	if firstHits == 0 {
		t.Fatal("first tick must fetch artifacts")
	}
	rows, _ := store.ListCacheEntries(db.CacheFilter{})
	if len(rows) != 1 || rows[0].Verified == nil || !*rows[0].Verified {
		t.Fatalf("first tick must land one verified=1 row, got %+v", rows)
	}

	// Second tick: the version is settled (bytes present + cached=1), so the
	// reconciler must skip it — no additional artifact fetches.
	if err := reconcileTarget(t.Context(), store, 4, *tgt); err != nil {
		t.Fatal(err)
	}
	if got := artifactHits.Load(); got != firstHits {
		t.Fatalf("second tick re-downloaded a settled version: artifact hits %d → %d (want no increase)", firstHits, got)
	}
	vers, _ := store.ListTargetVersions(tid)
	var stillCached bool
	for _, v := range vers {
		if v.Version == "44.0.0.0" && v.Cached {
			stillCached = true
		}
	}
	if !stillCached {
		t.Fatalf("settled version must remain cached=1 across ticks, got %+v", vers)
	}
}

// TestLandArtifact_FlatcarShapeValidSignatureLandsUnderWarn closes the coverage
// gap the retention tests leave: they run under `off` (dodging Flatcar's
// production keyring + fake .sig), so Flatcar's SIGNATURE-based land shape
// (SigURL + GPGKey, NO sha256) is never exercised under the DEFAULT `warn`.
// A subtly-wrong key/sig scheme would silently classForgery every artifact →
// rejected → Flatcar stops caching under warn, uncaught. This drives a
// Flatcar-shaped two-file set with GENUINE valid detached signatures (throwaway
// keyring via gpgFixture) through landArtifact under warn, then aggregates the
// verdicts, proving the whole shape lands with classPass and version-level
// verified=true. (Throwaway keys, not the production key, so this proves the
// land/verify WIRING for the shape — not Flatcar's actual upstream key/sig,
// which is not hermetically testable.)
func TestLandArtifact_FlatcarShapeValidSignatureLandsUnderWarn(t *testing.T) {
	// Flatcar's two-file PXE set; each file gets its own valid detached sig.
	files := map[string][]byte{
		"flatcar_production_pxe.vmlinuz":       []byte("vmlinuz-bytes"),
		"flatcar_production_pxe_image.cpio.gz": []byte("cpio-bytes"),
	}
	art := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		_, _ = w.Write(files[name])
	}))
	t.Cleanup(art.Close)

	dir := t.TempDir()
	verdicts := make([]artifactVerdict, 0, len(files))
	for name, contents := range files {
		keyring, sigURL, closeFn := gpgFixture(t, contents) // valid sig over the served bytes
		t.Cleanup(closeFn)
		a := ostype.Artifact{Filename: name, URL: art.URL + "/" + name, SigURL: sigURL, GPGKey: keyring}
		landed, v, err := landArtifact(t.Context(), dir, a, "warn")
		if err != nil {
			t.Fatalf("landArtifact %s: %v", name, err)
		}
		if !landed || v.class != classPass {
			t.Fatalf("%s: landed=%v class=%d, want landed=true class=%d (classPass)", name, landed, v.class, classPass)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s: valid-sig artifact must exist at final path: %v", name, err)
		}
		verdicts = append(verdicts, v)
	}

	verified, verifyErr := aggregateVerdicts(verdicts)
	if verified == nil || !*verified {
		t.Fatalf("flatcar-shaped valid sigs must aggregate to verified=true, got %v", verified)
	}
	if verifyErr != "" {
		t.Fatalf("valid signatures must produce no verify_err, got %q", verifyErr)
	}
}

// TestReconcileFCOSVerification exercises the reconcile admission/atomicity
// branch hermetically: an FCOS streams+artifacts server whose declared sha256
// either matches (land, verified=1) or mismatches (under strict → reject:
// version dir removed + a size=0/in_window=0/verified=0 failure row; under warn
// → land + verified=0). No GPG/embedded key needed — sha256 is enough to drive
// both branches through the REAL fedoraCoreOS.Artifacts seam.
func TestReconcileFCOSVerification(t *testing.T) {
	body := []byte("fcos-artifact")
	good := hexSHA(body)

	fcosServer := func(sha string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".json") {
				_, _ = fmt.Fprintf(w, `{"architectures":{"x86_64":{"artifacts":{"metal":{`+
					`"release":"44.0.0.0","formats":{"pxe":{`+
					`"kernel":{"location":"%[1]s/44/kernel","sha256":"%[2]s"},`+
					`"initramfs":{"location":"%[1]s/44/initramfs","sha256":"%[2]s"},`+
					`"rootfs":{"location":"%[1]s/44/rootfs","sha256":"%[2]s"}`+
					`}}}}}}}`, "http://"+r.Host, sha)
				return
			}
			_, _ = w.Write(body)
		}))
	}

	setup := func(t *testing.T, policy, sha string) (*db.Store, db.Target, *httptest.Server) {
		srv := fcosServer(sha)
		viper.Reset()
		viper.Set(config.DataDir, t.TempDir())
		viper.Set(config.CoreOSStreamsURL, srv.URL+"/%s.json")
		viper.Set(config.CoreOSArchitecture, "x86_64")
		viper.Set(config.CoreOSChannel, "stable")
		viper.Set(config.SignaturePolicy, policy)
		store := newReconcileStore(t)
		tid, err := store.CreateTarget(db.Target{OS: "fedora-coreos", Arch: "x86_64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		tgt, _ := store.GetTarget(tid)
		return store, *tgt, srv
	}

	t.Run("matching sha lands verified=1", func(t *testing.T) {
		store, tgt, srv := setup(t, "warn", good)
		t.Cleanup(srv.Close)
		t.Cleanup(viper.Reset)
		if err := reconcileTarget(t.Context(), store, 4, tgt); err != nil {
			t.Fatal(err)
		}
		rows, _ := store.ListCacheEntries(db.CacheFilter{})
		if len(rows) != 1 || rows[0].Verified == nil || !*rows[0].Verified || !rows[0].InWindow {
			t.Fatalf("want one in-window verified=1 row, got %+v", rows)
		}
		if !cacheDirExists("coreos", "stable", "x86_64", "44.0.0.0") {
			t.Fatal("verified version must land on disk")
		}
	})

	t.Run("strict rejects mismatch: dir removed + failure row", func(t *testing.T) {
		store, tgt, srv := setup(t, "strict", hexSHA([]byte("wrong")))
		t.Cleanup(srv.Close)
		t.Cleanup(viper.Reset)
		if err := reconcileTarget(t.Context(), store, 4, tgt); err != nil {
			t.Fatal(err)
		}
		if cacheDirExists("coreos", "stable", "x86_64", "44.0.0.0") {
			t.Fatal("strict must remove the rejected version dir (atomicity)")
		}
		rows, _ := store.ListCacheEntries(db.CacheFilter{})
		if len(rows) != 1 || rows[0].InWindow || rows[0].Size != 0 || rows[0].Verified == nil || *rows[0].Verified || rows[0].VerifyErr == "" {
			t.Fatalf("want a size=0/in_window=0/verified=0 failure row with an err, got %+v", rows)
		}
		vers, _ := store.ListTargetVersions(tgt.ID)
		for _, v := range vers {
			if v.Version == "44.0.0.0" && v.Cached {
				t.Fatal("rejected version must not be marked cached")
			}
		}
	})

	t.Run("warn lands mismatch with verified=0", func(t *testing.T) {
		store, tgt, srv := setup(t, "warn", hexSHA([]byte("wrong")))
		t.Cleanup(srv.Close)
		t.Cleanup(viper.Reset)
		if err := reconcileTarget(t.Context(), store, 4, tgt); err != nil {
			t.Fatal(err)
		}
		if !cacheDirExists("coreos", "stable", "x86_64", "44.0.0.0") {
			t.Fatal("warn must land a checksum-mismatch (corruption) version")
		}
		rows, _ := store.ListCacheEntries(db.CacheFilter{})
		if len(rows) != 1 || !rows[0].InWindow || rows[0].Verified == nil || *rows[0].Verified {
			t.Fatalf("want an in-window verified=0 row, got %+v", rows)
		}
	})

	// The whole point of `warn` is to WARN the operator: a corruption/checksum
	// failure that LANDS with verified=0 must also emit a WARN log line, not just
	// flip a silent DB flag. Capture the default logger and assert the WARN fires,
	// carrying the version and verify_err. (Only reachable under warn: strict
	// rejects, off records verified=NULL.)
	t.Run("warn logs a WARN when a mismatch lands", func(t *testing.T) {
		store, tgt, srv := setup(t, "warn", hexSHA([]byte("wrong")))
		t.Cleanup(srv.Close)
		t.Cleanup(viper.Reset)

		var logbuf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
		t.Cleanup(func() { slog.SetDefault(prev) })

		if err := reconcileTarget(t.Context(), store, 4, tgt); err != nil {
			t.Fatal(err)
		}

		logged := logbuf.String()
		if !strings.Contains(logged, "failed verification") {
			t.Fatalf("warn policy must emit a WARN when a mismatch lands; got log: %q", logged)
		}
		if !strings.Contains(logged, "level=WARN") {
			t.Fatalf("verification-failure log must be at WARN level; got: %q", logged)
		}
		if !strings.Contains(logged, "44.0.0.0") {
			t.Fatalf("WARN must identify the landed version; got: %q", logged)
		}
		if !strings.Contains(logged, "verifyErr=") {
			t.Fatalf("WARN must carry verifyErr; got: %q", logged)
		}
	})

	// Reconcile-level rejection + prior-version survival (D13/AC#4). The newest
	// version (44.0.0.0) fails verification under strict → rejected: its dir is
	// removed and a failure-visibility row is written. A PRIOR good version
	// (43.0.0.0), already cached on disk, must SURVIVE so NewestCached falls back
	// to it and the boot path never 404s. This drives the class-agnostic reconcile
	// rejection/atomicity path (see Step-1 mechanism note: forgery-under-warn
	// itself is proven at the landArtifact unit level, not reachable here).
	t.Run("reject removes newest but preserves the prior cached version", func(t *testing.T) {
		store, tgt, srv := setup(t, "strict", hexSHA([]byte("wrong")))
		t.Cleanup(srv.Close)
		t.Cleanup(viper.Reset)

		// Seed a prior good cached version on disk + in the DB (the version that is
		// currently serving before the newest lands).
		if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: tgt.ID, Version: "43.0.0.0", Source: "discovered", Cached: true}); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(cacheDir("coreos", "stable", "x86_64", "43.0.0.0"), 0o755); err != nil {
			t.Fatal(err)
		}
		priorID, _ := store.TargetVersionID(tgt.ID, "43.0.0.0")
		if err := store.UpsertCacheEntry(priorID, 1000); err != nil {
			t.Fatal(err)
		}

		if err := reconcileTarget(t.Context(), store, 4, tgt); err != nil {
			t.Fatal(err)
		}

		// Rejected newest: dir gone + a size=0/in_window=0/verified=0 failure row.
		if cacheDirExists("coreos", "stable", "x86_64", "44.0.0.0") {
			t.Fatal("strict must remove the rejected newest version dir (atomicity)")
		}
		byVer := map[string]db.CacheEntryRow{}
		rows, _ := store.ListCacheEntries(db.CacheFilter{})
		for _, r := range rows {
			byVer[r.Version] = r
		}
		rej, ok := byVer["44.0.0.0"]
		if !ok || rej.InWindow || rej.Size != 0 || rej.Verified == nil || *rej.Verified || rej.VerifyErr == "" {
			t.Fatalf("want a size=0/in_window=0/verified=0 failure row for the rejected version, got %+v", rej)
		}
		// The prior version's dir must survive — the boot fallback depends on it.
		if !cacheDirExists("coreos", "stable", "x86_64", "43.0.0.0") {
			t.Fatal("prior cached version dir must survive the newest version's rejection (D13/AC#4)")
		}
	})
}
