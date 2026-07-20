package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrate_CreatesTablesAndSetsUserVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "booty.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	for _, table := range []string{"targets", "target_versions", "meta", "hosts", "cache_entries"} {
		var name string
		err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not created: %v", table, err)
		}
	}

	var uv int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if uv != 9 {
		t.Errorf("user_version = %d, want 9 after all migrations", uv)
	}
}

func TestMigrate_IsIdempotentAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "booty.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	// Reopening the same file must not re-run migrations or error.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { s2.Close() })

	var uv int
	if err := s2.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if uv != 9 {
		t.Errorf("user_version = %d after reopen, want 9", uv)
	}
}

func TestMigration0003ConfigsRoles(t *testing.T) {
	s := newTestStore(t) // Open() runs every migration, incl. 0003

	// user_version reached 9 (nine migrations applied).
	var uv int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if uv != 9 {
		t.Fatalf("user_version = %d, want 9", uv)
	}

	// The four new tables + the hosts.config_id column exist.
	for _, tbl := range []string{"configs", "config_revisions", "roles", "host_roles"} {
		var name string
		err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", tbl, err)
		}
	}
	var cnt int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name='config_id'`).Scan(&cnt); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if cnt != 1 {
		t.Errorf("hosts.config_id column count = %d, want 1", cnt)
	}
}

func TestMigration0004SchematicKindAndColumn(t *testing.T) {
	s := newTestStore(t) // Open() runs every migration, incl. 0004

	// The new nullable column exists on config_revisions.
	var cnt int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('config_revisions') WHERE name='derived_schematic_id'`).Scan(&cnt); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if cnt != 1 {
		t.Errorf("derived_schematic_id column count = %d, want 1", cnt)
	}

	// The rebuilt kind CHECK admits 'schematic' and still rejects junk.
	if _, err := s.db.Exec(`INSERT INTO configs (name, kind) VALUES ('sch', 'schematic')`); err != nil {
		t.Errorf("kind='schematic' rejected after 0004: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO configs (name, kind) VALUES ('bad', 'bogus')`); err == nil {
		t.Error("kind='bogus' accepted; the rebuilt CHECK is missing")
	}
}

func TestMigration0004PreservesP4Data(t *testing.T) {
	// Build a v3 database BY HAND (raw handle, foreign_keys ON like production),
	// seed P4 rows, close, then Open() so ONLY 0004 runs — proving the configs
	// rebuild preserves configs/config_revisions/roles (and their IDs) across
	// the upgrade instead of cascade-wiping them via DROP TABLE's implicit
	// DELETE (the hazard the FK-off migration connection exists to prevent).
	path := filepath.Join(t.TempDir(), "upgrade.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	for _, m := range []string{"0001_init.sql", "0002_cache_entries.sql", "0003_configs_roles.sql"} {
		stmt, rerr := migrationsFS.ReadFile("migrations/" + m)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if _, err := raw.Exec(string(stmt)); err != nil {
			t.Fatalf("apply %s: %v", m, err)
		}
	}
	for _, stmt := range []string{
		`PRAGMA user_version = 3`,
		`INSERT INTO configs (name, kind) VALUES ('prod', 'butane')`,
		`INSERT INTO config_revisions (config_id, revision, source_b64, source_sha256) VALUES (1, 1, 'djE=', 'h1')`,
		`UPDATE configs SET active_revision_id = 1 WHERE id = 1`,
		`INSERT INTO roles (name, default_config_id) VALUES ('cp', 1)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path) // applies only 0004
	if err != nil {
		t.Fatalf("Open at v3: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	c, err := s.GetConfig(1)
	if err != nil || c.Name != "prod" || c.Kind != "butane" || !c.ActiveRevisionID.Valid || c.ActiveRevisionID.Int64 != 1 {
		t.Fatalf("config after rebuild = %+v, err %v", c, err)
	}
	rev, err := s.GetActiveRevision(1)
	if err != nil || rev.SourceB64 != "djE=" {
		t.Fatalf("revision after rebuild = %+v, err %v", rev, err)
	}
	r, err := s.GetRole(1)
	if err != nil || !r.DefaultConfigID.Valid || r.DefaultConfigID.Int64 != 1 {
		t.Fatalf("role after rebuild = %+v, err %v", r, err)
	}
	if _, err := s.CreateConfig("vanilla-check", "schematic"); err != nil {
		t.Fatalf("kind='schematic' rejected on an upgraded DB: %v", err)
	}
}

func TestMigration0005Clusters(t *testing.T) {
	s := newTestStore(t) // Open() runs every migration, incl. 0005

	// New tables exist.
	for _, tbl := range []string{"clusters", "cluster_node_configs"} {
		var name string
		if err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name); err != nil {
			t.Errorf("table %q missing: %v", tbl, err)
		}
	}

	// The three new nullable hosts columns exist.
	for _, col := range []string{"cluster_id", "machine_type", "node_config_id"} {
		var cnt int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name=?`, col).Scan(&cnt); err != nil {
			t.Fatalf("pragma_table_info(%s): %v", col, err)
		}
		if cnt != 1 {
			t.Errorf("hosts.%s column count = %d, want 1", col, cnt)
		}
	}

	// The rebuilt kind CHECK admits 'taloscluster' and still rejects junk.
	if _, err := s.db.Exec(`INSERT INTO configs (name, kind) VALUES ('tc', 'taloscluster')`); err != nil {
		t.Errorf("kind='taloscluster' rejected after 0005: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO configs (name, kind) VALUES ('bad5', 'bogus')`); err == nil {
		t.Error("kind='bogus' accepted; the rebuilt CHECK is missing")
	}

	// The nullable host_patch column exists on cluster_node_configs (persists
	// the per-host strategic-merge patch that produced the frozen bytes).
	var patchCnt int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('cluster_node_configs') WHERE name='host_patch'`).Scan(&patchCnt); err != nil {
		t.Fatalf("pragma_table_info(host_patch): %v", err)
	}
	if patchCnt != 1 {
		t.Errorf("cluster_node_configs.host_patch column count = %d, want 1", patchCnt)
	}

	// cluster_node_configs.source CHECK admits only generated|imported.
	if _, err := s.db.Exec(
		`INSERT INTO clusters (name, endpoint, talos_version, k8s_version, secrets_enc)
		 VALUES ('c1', 'https://10.0.0.10:6443', 'v1.13.5', 'v1.34.0', X'00')`); err != nil {
		t.Fatalf("insert cluster: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO cluster_node_configs (mac, cluster_id, revision, config_enc, sha256, source, host_patch)
		 VALUES ('aa:bb:cc:dd:ee:01', 1, 1, X'00', 'h', 'generated', 'machine:\n  certSANs: [1.2.3.4]\n')`); err != nil {
		t.Errorf("source='generated' with host_patch rejected: %v", err)
	}
	// host_patch is nullable (imported rows and patch-less binds store NULL).
	if _, err := s.db.Exec(
		`INSERT INTO cluster_node_configs (mac, cluster_id, revision, config_enc, sha256, source)
		 VALUES ('aa:bb:cc:dd:ee:02', 1, 1, X'00', 'h', 'imported')`); err != nil {
		t.Errorf("NULL host_patch rejected: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO cluster_node_configs (mac, cluster_id, revision, config_enc, sha256, source)
		 VALUES ('aa:bb:cc:dd:ee:01', 1, 2, X'00', 'h', 'bogus')`); err == nil {
		t.Error("source='bogus' accepted; CHECK missing")
	}
}

func TestMigration0005PreservesP5Data(t *testing.T) {
	// Build a v4 database BY HAND (raw handle, foreign_keys ON like production),
	// seed P4+P5 rows, close, then Open() so ONLY 0005 runs — proving the second
	// configs rebuild preserves configs/config_revisions (incl. the P5
	// derived_schematic_id) and roles across the upgrade.
	path := filepath.Join(t.TempDir(), "upgrade5.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	for _, m := range []string{"0001_init.sql", "0002_cache_entries.sql", "0003_configs_roles.sql", "0004_schematic_id.sql"} {
		stmt, rerr := migrationsFS.ReadFile("migrations/" + m)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if _, err := raw.Exec(string(stmt)); err != nil {
			t.Fatalf("apply %s: %v", m, err)
		}
	}
	for _, stmt := range []string{
		`PRAGMA user_version = 4`,
		`INSERT INTO configs (name, kind) VALUES ('sch', 'schematic')`,
		`INSERT INTO config_revisions (config_id, revision, source_b64, source_sha256, derived_schematic_id) VALUES (1, 1, 'djE=', 'h1', 'abc123')`,
		`UPDATE configs SET active_revision_id = 1 WHERE id = 1`,
		`INSERT INTO roles (name, default_config_id) VALUES ('cp', 1)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path) // applies only 0005
	if err != nil {
		t.Fatalf("Open at v4: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	c, err := s.GetConfig(1)
	if err != nil || c.Name != "sch" || c.Kind != "schematic" || !c.ActiveRevisionID.Valid || c.ActiveRevisionID.Int64 != 1 {
		t.Fatalf("config after rebuild = %+v, err %v", c, err)
	}
	rev, err := s.GetActiveRevision(1)
	if err != nil || rev.SourceB64 != "djE=" || rev.DerivedSchematicID == nil || *rev.DerivedSchematicID != "abc123" {
		t.Fatalf("revision after rebuild = %+v, err %v", rev, err)
	}
	if _, err := s.CreateConfig("tc-check", "taloscluster"); err != nil {
		t.Fatalf("kind='taloscluster' rejected on an upgraded DB: %v", err)
	}
}

func TestMigration0006DebianConfig(t *testing.T) {
	s := newTestStore(t) // Open() runs every migration, incl. 0006

	var uv int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if uv != 9 {
		t.Fatalf("user_version = %d, want 9", uv)
	}

	// The rebuilt kind CHECK admits 'debianconfig' and still rejects junk.
	if _, err := s.db.Exec(`INSERT INTO configs (name, kind) VALUES ('dc', 'debianconfig')`); err != nil {
		t.Errorf("kind='debianconfig' rejected after 0006: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO configs (name, kind) VALUES ('bad6', 'bogus')`); err == nil {
		t.Error("kind='bogus' accepted; the rebuilt CHECK is missing")
	}
}

func TestMigration0006PreservesData(t *testing.T) {
	// Build a v5 database BY HAND (raw handle, foreign_keys ON like production),
	// seed rows, close, then Open() so ONLY 0006 runs — proving the third
	// configs rebuild preserves configs/config_revisions/roles across the
	// upgrade instead of cascade-wiping them via DROP TABLE's implicit DELETE.
	path := filepath.Join(t.TempDir(), "upgrade6.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	for _, m := range []string{"0001_init.sql", "0002_cache_entries.sql", "0003_configs_roles.sql", "0004_schematic_id.sql", "0005_clusters.sql"} {
		stmt, rerr := migrationsFS.ReadFile("migrations/" + m)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if _, err := raw.Exec(string(stmt)); err != nil {
			t.Fatalf("apply %s: %v", m, err)
		}
	}
	for _, stmt := range []string{
		`PRAGMA user_version = 5`,
		// 'taloscluster' (not 'preseed'): must be valid under the v5-era CHECK
		// at seed time (before 0006 even adds 'debianconfig' to it) AND survive
		// the 0009 rebuild that later drops 'preseed' from the CHECK.
		`INSERT INTO configs (name, kind) VALUES ('tc', 'taloscluster')`,
		`INSERT INTO config_revisions (config_id, revision, source_b64, source_sha256) VALUES (1, 1, 'djE=', 'h1')`,
		`UPDATE configs SET active_revision_id = 1 WHERE id = 1`,
		`INSERT INTO roles (name, default_config_id) VALUES ('deb', 1)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path) // applies 0006 onward, incl. 0009
	if err != nil {
		t.Fatalf("Open at v5: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	c, err := s.GetConfig(1)
	if err != nil || c.Name != "tc" || c.Kind != "taloscluster" || !c.ActiveRevisionID.Valid || c.ActiveRevisionID.Int64 != 1 {
		t.Fatalf("config after rebuild = %+v, err %v", c, err)
	}
	rev, err := s.GetActiveRevision(1)
	if err != nil || rev.SourceB64 != "djE=" {
		t.Fatalf("revision after rebuild = %+v, err %v", rev, err)
	}
	r, err := s.GetRole(1)
	if err != nil || !r.DefaultConfigID.Valid || r.DefaultConfigID.Int64 != 1 {
		t.Fatalf("role after rebuild = %+v, err %v", r, err)
	}
	if _, err := s.CreateConfig("dc-check", "debianconfig"); err != nil {
		t.Fatalf("kind='debianconfig' rejected on an upgraded DB: %v", err)
	}
}

func TestMigrate0007_ColumnAndCheck(t *testing.T) {
	s := newTestStore(t) // existing helper; opens + migrates fully
	if _, err := s.db.Exec(`INSERT INTO targets (os,arch,params,mode,retain_n,source,enabled)
		VALUES ('talos','amd64','{"schematic":"x"}','discovery',3,'catalog',1)`); err != nil {
		t.Fatalf("insert catalog row: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO targets (os,arch,params,mode,retain_n,source,enabled)
		VALUES ('talos','amd64','{"schematic":"y"}','discovery',3,'bogus',1)`); err == nil {
		t.Fatal("expected CHECK to reject source='bogus'")
	}
}

// TestMigrate0007BackfillsSource_Upgrade drives the CASE backfill on REAL data,
// following the hand-built-DB upgrade pattern used by TestMigration0006*. It
// applies 0001..0006 by hand, seeds one row per source class + a matching host,
// sets user_version=6, then Open() runs 0007 and we assert the backfill.
func TestMigrate0007BackfillsSource_Upgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade7.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	for _, m := range []string{
		"0001_init.sql", "0002_cache_entries.sql", "0003_configs_roles.sql",
		"0004_schematic_id.sql", "0005_clusters.sql", "0006_debianconfig.sql",
	} {
		stmt, rerr := migrationsFS.ReadFile("migrations/" + m)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if _, err := raw.Exec(string(stmt)); err != nil {
			t.Fatalf("apply %s: %v", m, err)
		}
	}
	if _, err := raw.Exec(`PRAGMA user_version = 6`); err != nil {
		t.Fatal(err)
	}

	// predefined flatcar row -> expect source='catalog'
	if _, err := raw.Exec(`INSERT INTO targets (os,arch,params,mode,retain_n,predefined,enabled)
		VALUES ('flatcar','amd64','{"channel":"stable"}','discovery',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	// host-derived talos schematic (predefined=0) with a MATCHING hosts row -> 'host'
	if _, err := raw.Exec(`INSERT INTO targets (os,arch,params,mode,retain_n,predefined,enabled)
		VALUES ('talos','amd64','{"schematic":"HS"}','discovery',3,0,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO hosts (mac,os,schematic) VALUES ('aa:bb','talos','HS')`); err != nil {
		t.Fatal(err)
	}
	// plain operator/api row (predefined=0, no host match) -> 'api'
	if _, err := raw.Exec(`INSERT INTO targets (os,arch,params,mode,retain_n,predefined,enabled)
		VALUES ('talos','amd64','{"schematic":"ORPHAN"}','discovery',3,0,1)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path) // runs 0007
	if err != nil {
		t.Fatalf("Open (0007): %v", err)
	}
	defer store.Close()
	got := map[string]string{} // params -> source
	rows, _ := store.db.Query(`SELECT params, source FROM targets`)
	defer rows.Close()
	for rows.Next() {
		var p, s string
		rows.Scan(&p, &s)
		got[p] = s
	}
	for params, want := range map[string]string{
		`{"channel":"stable"}`:   "catalog",
		`{"schematic":"HS"}`:     "host",
		`{"schematic":"ORPHAN"}`: "api",
	} {
		if got[params] != want {
			t.Errorf("source for %s = %q, want %q", params, got[params], want)
		}
	}
}

func TestMigrate0008_DebianColumns(t *testing.T) {
	s := newTestStore(t) // existing helper: opens + migrates fully
	var uv int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil {
		t.Fatal(err)
	}
	if uv != 9 {
		t.Fatalf("user_version = %d, want 9", uv)
	}
	// defaults apply to a row inserted without the new columns
	if _, err := s.db.Exec(`INSERT INTO targets (os,arch,params,mode,retain_n,source,enabled)
		VALUES ('debian','amd64','{"channel":"13"}','discovery',3,'catalog',1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var sm, dm sql.NullString
	var dc int
	if err := s.db.QueryRow(`SELECT source_mode, dvd_count, desired_mode FROM targets
		WHERE params='{"channel":"13"}'`).Scan(&sm, &dc, &dm); err != nil {
		t.Fatal(err)
	}
	if sm.String != "netinst" || dc != 1 || dm.Valid {
		t.Fatalf("defaults: source_mode=%q dvd_count=%d desired_mode(valid=%v)=%q, want netinst/1/NULL",
			sm.String, dc, dm.Valid, dm.String)
	}
	// CHECK rejects a bad source_mode
	if _, err := s.db.Exec(`UPDATE targets SET source_mode='bogus' WHERE params='{"channel":"13"}'`); err == nil {
		t.Fatal("expected CHECK to reject source_mode='bogus'")
	}
}

// seedPreDropDB builds a v8 database BY HAND (raw handle, foreign_keys ON like
// production): applies migrations 0001..0008, sets PRAGMA user_version=8, then
// inserts a legacy 'preseed' config row while the CHECK still admits it (0009
// hasn't rebuilt the table yet). Modeled on the TestMigration0006PreservesData
// harness above.
func seedPreDropDB(t *testing.T, dbPath string) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	for _, m := range []string{
		"0001_init.sql", "0002_cache_entries.sql", "0003_configs_roles.sql",
		"0004_schematic_id.sql", "0005_clusters.sql", "0006_debianconfig.sql",
		"0007_target_source.sql", "0008_debian_source_mode.sql",
	} {
		stmt, rerr := migrationsFS.ReadFile("migrations/" + m)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if _, err := raw.Exec(string(stmt)); err != nil {
			t.Fatalf("apply %s: %v", m, err)
		}
	}
	for _, stmt := range []string{
		`PRAGMA user_version = 8`,
		`INSERT INTO configs (name, kind) VALUES ('legacy-ps', 'preseed')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMigratePreseedRemovalBlocksOnExistingRow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "b.db")

	// Raw handle: migrate to just BEFORE 0009 and seed a legacy preseed row
	// while the CHECK still admits it.
	seedPreDropDB(t, dbPath)

	// Now Open() — migrate() runs preflightPreseedRemoval at current=8 (<9) and
	// must abort naming the converter and the offending config.
	_, err := Open(dbPath)
	if err == nil {
		t.Fatal("expected startup to abort on the surviving preseed row")
	}
	if !strings.Contains(err.Error(), "convert-preseed") || !strings.Contains(err.Error(), "legacy-ps") {
		t.Fatalf("error must name the converter and the offending config: %v", err)
	}
}

func TestMigratePreseedRemovalCleanWhenNoPreseedRows(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	// A direct insert of kind='preseed' is now rejected by the 0009 CHECK.
	if _, err := s.db.Exec(`INSERT INTO configs (name, kind) VALUES ('x', 'preseed')`); err == nil {
		t.Fatal("expected CHECK to reject kind='preseed' after 0009")
	}
}
