package cache

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// This file exercises the assembled declarative-catalog-config feature
// end-to-end at the reconcile layer, per design §9 (restart idempotency) and
// §11 (testing: idempotency, round-trip, back-compat). No production code is
// touched here — only acceptance coverage over `applyCatalog`, `defaultCatalog`,
// `LoadCatalog`, and `parseCatalog`.

// viperSetDefaultFlags sets viper to the default flag values the catalog
// loader reads when no operator override is present. Mirrors the
// viper.Reset()/t.Cleanup(viper.Reset) pattern already used by
// TestDefaultCatalog_CuratedSet in catalog_test.go.
func viperSetDefaultFlags(t *testing.T) {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosSchematic, config.DefaultTalosSchematic)
	viper.Set(config.TalosRetainMinors, 3)
}

// TestCatalog_RestartDownloadsNothingWhenCached is the design §9 acceptance
// requirement: reconciling a catalog whose targets already have their newest
// version cached on disk must perform no artifact fetch, and reloading the
// same catalog (simulating a process restart) must not re-create the row.
//
// This adapts the fake-Flatcar-server-with-request-counter technique already
// established in reconcile_test.go (newFlatcarFixture / TestReconcileFlatcar*
// for the server+viper setup, TestReconcileSkipsAlreadyCachedVersion for the
// hit-counter idiom) rather than calling those helpers directly: neither
// exposes a request counter, and the row here must be created via
// applyCatalog (not a raw store.CreateTarget) so the test also proves the
// catalog-driven create path is what's being re-run across the simulated
// restart.
func TestCatalog_RestartDownloadsNothingWhenCached(t *testing.T) {
	version := "100.0.0"
	var artifactHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/version.txt") {
			_, _ = w.Write([]byte("FLATCAR_VERSION=" + version + "\n"))
			return
		}
		artifactHits.Add(1)
		_, _ = w.Write([]byte("artifact-bytes")) // vmlinuz / cpio.gz
	}))
	t.Cleanup(srv.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.FlatcarURL, srv.URL+"/%s/%s")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.SignaturePolicy, "off") // retention/idempotency, not signature, is under test here

	store := seedTestStore(t)
	entries := []CatalogEntry{{OS: "flatcar", Arch: "amd64", Retain: new(1), Spec: map[string]string{"channel": "stable"}}}
	if err := applyCatalog(store, entries); err != nil {
		t.Fatalf("applyCatalog (initial boot): %v", err)
	}

	tgt, ok := targetByOSParams(t, store, "flatcar", mustEncode(t, map[string]string{"channel": "stable"}))
	if !ok {
		t.Fatal("applyCatalog did not create the flatcar/stable target")
	}

	// First boot: reconcile discovers 100.0.0 and downloads it.
	if err := reconcileTarget(t.Context(), store, 4, tgt); err != nil {
		t.Fatalf("reconcileTarget (first boot): %v", err)
	}
	firstHits := artifactHits.Load()
	if firstHits == 0 {
		t.Fatal("first boot must fetch artifacts")
	}
	if !cacheDirExists("flatcar", "stable", "amd64", "100.0.0") {
		t.Fatal("first boot must cache 100.0.0 on disk")
	}

	// Restart: reload catalog.yaml (unchanged) and re-apply it, exactly as
	// startup does. Identity (os,arch,params) is unchanged, so this must not
	// create a second row.
	if err := applyCatalog(store, entries); err != nil {
		t.Fatalf("applyCatalog (restart): %v", err)
	}
	all, err := store.ListTargets()
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("restart must not create a second row, got %d targets: %+v", len(all), all)
	}
	if all[0].ID != tgt.ID {
		t.Fatalf("restart re-keyed the target: was id=%d, now id=%d", tgt.ID, all[0].ID)
	}

	// Second boot: the same version is already cached on disk, so reconcile
	// must fetch nothing more.
	if err := reconcileTarget(t.Context(), store, 4, all[0]); err != nil {
		t.Fatalf("reconcileTarget (second boot): %v", err)
	}
	if got := artifactHits.Load(); got != firstHits {
		t.Fatalf("restart re-downloaded a cached version: artifact hits %d -> %d (want no increase)", firstHits, got)
	}
}

// TestCatalog_UpgradeTransition_RevertsDeclaredFieldsKeepsMode is the design
// §9 "upgrade transition" (I3) acceptance requirement: a pre-upgrade
// source=catalog (ex-predefined) row that an operator PATCHed loses its
// PATCHed declared fields (enabled, retain_n) on the first post-upgrade apply,
// but keeps its non-declared mode.
func TestCatalog_UpgradeTransition_RevertsDeclaredFieldsKeepsMode(t *testing.T) {
	store := seedTestStore(t)
	// Post-0007 an ex-predefined row is source='catalog'. Operator had PATCHed
	// it: disabled, custom retain, mode=manual.
	id, err := store.CreateTarget(db.Target{OS: "flatcar", Arch: "amd64",
		Params: `{"channel":"stable"}`, Mode: "manual", RetainN: 9, Source: "catalog", Enabled: false})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}

	viperSetDefaultFlags(t)
	if err := applyCatalog(store, defaultCatalog()); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTarget(id)
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	// declared fields revert to the default (enabled=true, retain=1); mode kept.
	if !got.Enabled || got.RetainN != 1 || got.Mode != "manual" {
		t.Errorf("upgrade transition = %+v, want enabled=true retain=1 mode=manual", got)
	}
}

// TestCatalog_AbsentFileReproducesDefaultSet is the design §11 back-compat
// requirement: LoadCatalog with no catalog.yaml present, under default flags,
// reproduces the curated default set (Flatcar stable+lts, Talos; no FCOS).
func TestCatalog_AbsentFileReproducesDefaultSet(t *testing.T) {
	store := seedTestStore(t)
	viperSetDefaultFlags(t)
	viper.Set(config.CatalogFile, "")
	viper.Set(config.DataDir, t.TempDir()) // no catalog.yaml present

	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if err := applyCatalog(store, catalog); err != nil {
		t.Fatal(err)
	}
	all, err := store.ListTargets()
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("targets = %d, want 3 (flatcar stable+lts, talos)", len(all))
	}
	for _, want := range []struct{ os, arch, params string }{
		{"flatcar", "amd64", `{"channel":"stable"}`},
		{"flatcar", "amd64", `{"channel":"lts"}`},
		{"talos", "amd64", `{"schematic":"` + config.DefaultTalosSchematic + `"}`},
	} {
		found := slices.ContainsFunc(all, func(tg db.Target) bool {
			return tg.OS == want.os && tg.Arch == want.arch && tg.Params == want.params && tg.Source == "catalog"
		})
		if !found {
			t.Errorf("missing default target %s/%s/%s", want.os, want.arch, want.params)
		}
	}
	// FCOS is not in the default set.
	if slices.ContainsFunc(all, func(tg db.Target) bool { return tg.OS == "fedora-coreos" }) {
		t.Error("fedora-coreos must NOT be created by the default catalog")
	}
}

// TestCatalog_RoundTripDesignExamples is the design §11 round-trip
// requirement: each §7 example parses, validates, and produces the expected
// target rows. Example 3 (Debian) is not yet catalog-expressible (design B1)
// and must be rejected by parseCatalog — this also confirms example 1's FCOS
// entry (dropped from the shipped default, TestCatalog_AbsentFileReproducesDefaultSet)
// remains fully usable via an explicit operator catalog.
func TestCatalog_RoundTripDesignExamples(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []struct{ os, arch, params string }
	}{
		{
			name: "example 1: shipped default (Flatcar/FCOS/Talos)",
			yaml: `schemaVersion: 1
catalog:
  - os: flatcar
    arch: amd64
    retain: 1
    spec: {channel: stable}
  - os: fedora-coreos
    arch: x86_64
    retain: 1
    spec: {channel: stable}
  - os: talos
    arch: amd64
    retain: 3
    spec: {schematic: 376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba}
`,
			want: []struct{ os, arch, params string }{
				{"flatcar", "amd64", `{"channel":"stable"}`},
				{"fedora-coreos", "x86_64", `{"channel":"stable"}`},
				{"talos", "amd64", `{"schematic":"376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba"}`},
			},
		},
		{
			name: "example 2: Talos-only homelab, both arches",
			yaml: `schemaVersion: 1
catalog:
  - os: talos
    arch: amd64
    retain: 3
    spec: {schematic: 376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba}
  - os: talos
    arch: arm64
    retain: 3
    spec: {schematic: 376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba}
`,
			want: []struct{ os, arch, params string }{
				{"talos", "amd64", `{"schematic":"376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba"}`},
				{"talos", "arm64", `{"schematic":"376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba"}`},
			},
		},
		{
			name: "example 4: Flatcar/FCOS multi-channel/arch",
			yaml: `schemaVersion: 1
catalog:
  - os: flatcar
    arch: amd64
    retain: 1
    spec: {channel: stable}
  - os: flatcar
    arch: amd64
    retain: 1
    spec: {channel: lts}
  - os: flatcar
    arch: arm64
    retain: 1
    spec: {channel: stable}
  - os: fedora-coreos
    arch: x86_64
    retain: 1
    spec: {channel: stable}
  - os: fedora-coreos
    arch: x86_64
    retain: 1
    spec: {channel: testing}
`,
			want: []struct{ os, arch, params string }{
				{"flatcar", "amd64", `{"channel":"stable"}`},
				{"flatcar", "amd64", `{"channel":"lts"}`},
				{"flatcar", "arm64", `{"channel":"stable"}`},
				{"fedora-coreos", "x86_64", `{"channel":"stable"}`},
				{"fedora-coreos", "x86_64", `{"channel":"testing"}`},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := parseCatalog([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("parseCatalog: %v", err)
			}
			store := seedTestStore(t)
			if err := applyCatalog(store, entries); err != nil {
				t.Fatalf("applyCatalog: %v", err)
			}
			all, err := store.ListTargets()
			if err != nil {
				t.Fatalf("ListTargets: %v", err)
			}
			if len(all) != len(tt.want) {
				t.Fatalf("targets = %d, want %d: %+v", len(all), len(tt.want), all)
			}
			byKey := indexByIdentity(all)
			for _, want := range tt.want {
				got, ok := byKey[identityKey(want.os, want.arch, want.params)]
				if !ok {
					t.Errorf("missing target %s/%s/%s", want.os, want.arch, want.params)
					continue
				}
				if got.Source != "catalog" || !got.Enabled {
					t.Errorf("target %s/%s/%s = %+v, want source=catalog enabled=true", want.os, want.arch, want.params, got)
				}
			}
		})
	}

	// Example 3 (Debian) is FORWARD-LOOKING per the design (§7, B1): Debian
	// target support does not exist yet, so parseCatalog must reject it, exactly
	// as TestParseCatalog_RejectsDebian (Task 2) already asserts for a minimal
	// debian entry. This drives the literal §7 example 3 text through the same
	// gate for full-document round-trip coverage.
	t.Run("example 3: Debian rejected (not yet catalog-expressible)", func(t *testing.T) {
		yaml := `schemaVersion: 1
catalog:
  - os: debian
    arch: amd64
    spec: {release: "13", sourceMode: netinst}
  - os: debian
    arch: amd64
    spec: {release: "12", sourceMode: dvd, dvdCount: 3}
  - os: debian
    arch: amd64
    enabled: false
    spec: {release: "11", sourceMode: dvd, dvdCount: 3}
`
		if _, err := parseCatalog([]byte(yaml)); err == nil {
			t.Fatal("want error: debian is not yet a supported catalog os (design B1)")
		}
	})
}
