package cache

import (
	"testing"

	"github.com/jeefy/booty/pkg/db"
)

// indexByIdentity builds a "os|arch|params" -> Target lookup over all rows, for
// asserting on rows created by applyCatalog without needing to know their id.
func indexByIdentity(targets []db.Target) map[string]db.Target {
	out := make(map[string]db.Target, len(targets))
	for _, tg := range targets {
		out[identityKey(tg.OS, tg.Arch, tg.Params)] = tg
	}
	return out
}

func mustEncode(t *testing.T, spec map[string]string) string {
	t.Helper()
	params, err := encodeParams(spec)
	if err != nil {
		t.Fatalf("encodeParams: %v", err)
	}
	return params
}

func TestApplyCatalog_CreatesUpdatesDisablesLeavesAlone(t *testing.T) {
	store := seedTestStore(t) // opens + migrates a temp DB (existing helper)

	// Pre-existing rows across all three sources.
	apiID, _ := store.CreateTarget(db.Target{OS: "flatcar", Arch: "amd64",
		Params: `{"channel":"beta"}`, Mode: "manual", RetainN: 5, Source: "api", Enabled: true})
	hostID, _ := store.CreateTarget(db.Target{OS: "talos", Arch: "amd64",
		Params: `{"schematic":"host1"}`, Mode: "discovery", RetainN: 3, Source: "host", Enabled: true})
	staleID, _ := store.CreateTarget(db.Target{OS: "fedora-coreos", Arch: "x86_64",
		Params: `{"channel":"testing"}`, Mode: "discovery", RetainN: 1, Source: "catalog", Enabled: true})

	entries := []CatalogEntry{
		{OS: "flatcar", Arch: "amd64", Spec: map[string]string{"channel": "stable"}},             // create
		{OS: "talos", Arch: "amd64", Retain: new(9), Spec: map[string]string{"schematic": "s2"}}, // create w/ retain
	}
	if err := applyCatalog(store, entries); err != nil {
		t.Fatalf("applyCatalog: %v", err)
	}

	all, _ := store.ListTargets()
	byKey := indexByIdentity(all) // test helper: os|arch|params -> Target

	// created as catalog, mode discovery, retain default 1 / explicit 9
	fc := byKey["flatcar|amd64|"+mustEncode(t, map[string]string{"channel": "stable"})]
	if fc.Source != "catalog" || fc.Mode != "discovery" || fc.RetainN != 1 || !fc.Enabled {
		t.Errorf("flatcar stable = %+v", fc)
	}
	// api row untouched (still beta, manual, retain 5)
	if a, _ := store.GetTarget(apiID); a.Source != "api" || a.Mode != "manual" || a.RetainN != 5 || !a.Enabled {
		t.Errorf("api row mutated: %+v", a)
	}
	// host row untouched
	if h, _ := store.GetTarget(hostID); h.Source != "host" || !h.Enabled {
		t.Errorf("host row mutated: %+v", h)
	}
	// stale catalog row (fcos/testing) disabled, not deleted
	if s, _ := store.GetTarget(staleID); s.Enabled || s.Source != "catalog" {
		t.Errorf("stale catalog row should be disabled+kept: %+v", s)
	}
}

func TestApplyCatalog_AdoptsMatchingApiRow(t *testing.T) {
	store := seedTestStore(t)
	id, _ := store.CreateTarget(db.Target{OS: "talos", Arch: "amd64",
		Params: `{"schematic":"s"}`, Mode: "manual", RetainN: 7, Source: "api", Enabled: false})
	entries := []CatalogEntry{{OS: "talos", Arch: "amd64", Retain: new(2), Spec: map[string]string{"schematic": "s"}}}
	if err := applyCatalog(store, entries); err != nil {
		t.Fatalf("applyCatalog: %v", err)
	}
	got, _ := store.GetTarget(id)
	// declared fields reconcile to catalog; mode (not declared) preserved.
	if got.Source != "catalog" || got.Enabled != true || got.RetainN != 2 || got.Mode != "manual" {
		t.Errorf("adopted row = %+v, want source=catalog enabled=true retain=2 mode=manual", got)
	}
}

func TestApplyCatalog_Idempotent_NoWritesSecondPass(t *testing.T) {
	store := seedTestStore(t)
	entries := []CatalogEntry{{OS: "flatcar", Arch: "amd64", Spec: map[string]string{"channel": "stable"}}}
	if err := applyCatalog(store, entries); err != nil {
		t.Fatal(err)
	}
	before, _ := store.GetTarget(1)
	if err := applyCatalog(store, entries); err != nil {
		t.Fatal(err)
	}
	after, _ := store.GetTarget(1)
	// db.Target does not scan updated_at, so assert field-equality: an identical
	// second pass must not change any declared field (UpdateTargetFromCatalog
	// only fires on a diff, so no UPDATE runs at all).
	if before.Source != after.Source || before.Enabled != after.Enabled ||
		before.RetainN != after.RetainN || before.Mode != after.Mode {
		t.Errorf("second identical apply mutated the row: before=%+v after=%+v", before, after)
	}
}

func TestApplyCatalog_SetsDebianModeColumnsOnCreate(t *testing.T) {
	store := seedTestStore(t)
	if err := applyCatalog(store, []CatalogEntry{
		{OS: "debian", Arch: "amd64", Enabled: new(false), Retain: new(1), Spec: map[string]string{"channel": "12"}, SourceMode: "dvd", DvdCount: 2},
	}); err != nil {
		t.Fatal(err)
	}
	p12 := mustEncode(t, map[string]string{"channel": "12"})
	got, err := store.GetTargetByIdentity("debian", "amd64", p12)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceMode != "dvd" || got.DvdCount != 2 {
		t.Fatalf("create must carry source_mode/dvd_count: %q/%d", got.SourceMode, got.DvdCount)
	}
}

func TestApplyCatalog_DoesNotRevertPromotedMode(t *testing.T) {
	store := seedTestStore(t)
	// 1. Create the row (source_mode defaults to netinst).
	if err := applyCatalog(store, []CatalogEntry{
		{OS: "debian", Arch: "amd64", Enabled: new(true), Retain: new(1), Spec: map[string]string{"channel": "13"}},
	}); err != nil {
		t.Fatal(err)
	}
	p13 := mustEncode(t, map[string]string{"channel": "13"})
	n, err := store.GetTargetByIdentity("debian", "amd64", p13)
	if err != nil {
		t.Fatal(err)
	}
	// 2. Simulate a completed promote: the reconciler flipped source_mode to dvd.
	if err := store.SetTargetSourceMode(n.ID, "dvd"); err != nil {
		t.Fatal(err)
	}
	// 3. Re-apply the SAME identity but with a CHANGED declared field (retain
	//    1 -> 2) so the diff-condition fires and UpdateTargetFromCatalog
	//    actually runs the UPDATE branch. (Re-applying an identical entry would
	//    short-circuit to a no-op and make the source_mode assertion below
	//    vacuous — it must pass BECAUSE the UPDATE preserves source_mode, not
	//    because nothing touched the row.)
	if err := applyCatalog(store, []CatalogEntry{
		{OS: "debian", Arch: "amd64", Enabled: new(true), Retain: new(2), Spec: map[string]string{"channel": "13"}},
	}); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetTargetByIdentity("debian", "amd64", p13)
	if err != nil {
		t.Fatal(err)
	}
	// The UPDATE genuinely ran (retain changed 1 -> 2) AND did NOT revert the
	// promoted source_mode. The retain assertion is what makes the source_mode
	// assertion non-vacuous: it proves UpdateTargetFromCatalog executed.
	if after.RetainN != 2 {
		t.Fatalf("UPDATE branch did not run: retain_n = %d, want 2", after.RetainN)
	}
	if after.SourceMode != "dvd" {
		t.Fatal("re-apply UPDATE must not revert a promoted source_mode (catalog owns declared fields only)")
	}
}
