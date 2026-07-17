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
