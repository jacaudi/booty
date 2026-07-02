package cache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

func migrateFixture(t *testing.T) *db.Store {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.CoreOSChannel, "stable")
	return seedTestStore(t)
}

func TestMigrateFreshInstallIsNoOp(t *testing.T) {
	store := migrateFixture(t)
	if err := MigrateChannelLayout(store); err != nil {
		t.Fatalf("fresh install must be a no-op: %v", err)
	}
	all, _ := store.ListTargets()
	if len(all) != 0 {
		t.Fatalf("no rows may appear, got %d", len(all))
	}
}

func TestMigrateRewritesOldShapeRowInPlace(t *testing.T) {
	store := migrateFixture(t)
	id, err := store.CreateTarget(db.Target{OS: "flatcar", Arch: "amd64", Params: "{}", Mode: "discovery", RetainN: 1, Predefined: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: id, Version: "4230.2.2", Source: "discovered", Cached: true}); err != nil {
		t.Fatal(err)
	}

	if err := MigrateChannelLayout(store); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got, err := store.GetTarget(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Params != `{"channel":"stable"}` || !got.Enabled {
		t.Fatalf("old-shape row must be rewritten in place and stay enabled: %+v", got)
	}
	vs, _ := store.ListTargetVersions(id)
	if len(vs) != 1 || vs[0].Version != "4230.2.2" {
		t.Fatalf("target_versions must be preserved: %+v", vs)
	}

	// Idempotent: a second run changes nothing.
	if err := MigrateChannelLayout(store); err != nil {
		t.Fatalf("migrate (2nd): %v", err)
	}
	again, _ := store.GetTarget(id)
	if *again != *got {
		t.Fatalf("second run must be a no-op: %+v vs %+v", again, got)
	}
}

func TestMigrateDisablesOldRowWhenDestinationExists(t *testing.T) {
	store := migrateFixture(t)
	oldID, _ := store.CreateTarget(db.Target{OS: "fedora-coreos", Arch: "x86_64", Params: "{}", Mode: "discovery", RetainN: 1, Predefined: true, Enabled: true})
	// Operator pre-created the destination row.
	if _, err := store.CreateTarget(db.Target{OS: "fedora-coreos", Arch: "x86_64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 2, Predefined: false, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	if err := MigrateChannelLayout(store); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	old, _ := store.GetTarget(oldID)
	if old.Enabled || old.Params != "{}" {
		t.Fatalf("old row must be DISABLED (never merged): %+v", old)
	}
}

func TestMigrateRenamesDashDirOnce(t *testing.T) {
	store := migrateFixture(t)
	root := cacheRoot()
	oldDir := filepath.Join(root, "flatcar", "-", "amd64", "4230.2.2")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "flatcar_production_pxe.vmlinuz"), []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MigrateChannelLayout(store); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	newFile := filepath.Join(root, "flatcar", "stable", "amd64", "4230.2.2", "flatcar_production_pxe.vmlinuz")
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("artifacts must be renamed under the channel segment: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "flatcar", "-")); !os.IsNotExist(err) {
		t.Fatal("old '-' dir must be gone after the rename")
	}
	// Idempotent: second run (dash dir absent) is a no-op.
	if err := MigrateChannelLayout(store); err != nil {
		t.Fatalf("migrate (2nd): %v", err)
	}
}

func TestMigrateLeavesDashWhenBothDirsExist(t *testing.T) {
	store := migrateFixture(t)
	root := cacheRoot()
	if err := os.MkdirAll(filepath.Join(root, "coreos", "-", "x86_64", "44.0.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "coreos", "stable", "x86_64", "44.1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MigrateChannelLayout(store); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "coreos", "-", "x86_64", "44.0.0.0")); err != nil {
		t.Fatal("when both exist, '-' must be left in place (WARN; scan reports orphans)")
	}
	if _, err := os.Stat(filepath.Join(root, "coreos", "stable", "x86_64", "44.1.0.0")); err != nil {
		t.Fatal("existing destination must be untouched")
	}
}

func TestMigrateDiskStepRunsEvenWhenDBAlreadyMigrated(t *testing.T) {
	// Crash-consistency (SGE #3): the disk step keys ONLY on directories, never
	// on DB/params shape — a crash between the two steps retries the remainder.
	store := migrateFixture(t)
	if _, err := store.CreateTarget(db.Target{OS: "flatcar", Arch: "amd64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 1, Predefined: true, Enabled: true}); err != nil {
		t.Fatal(err) // DB already in the new shape
	}
	oldDir := filepath.Join(cacheRoot(), "flatcar", "-", "amd64", "4230.2.2")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MigrateChannelLayout(store); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheRoot(), "flatcar", "stable", "amd64", "4230.2.2")); err != nil {
		t.Fatal("disk step must run regardless of DB shape (crash between steps)")
	}
}

func TestMigrateRejectsUnsafeChannelFlag(t *testing.T) {
	store := migrateFixture(t)
	viper.Set(config.FlatcarChannel, "../evil")
	if err := MigrateChannelLayout(store); err == nil {
		t.Fatal("a malformed channel flag must fail startup, not mint an unsafe segment")
	}
}
