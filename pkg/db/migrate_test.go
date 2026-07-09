package db

import (
	"database/sql"
	"path/filepath"
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
	if uv != 4 {
		t.Errorf("user_version = %d, want 4 after all migrations", uv)
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
	if uv != 4 {
		t.Errorf("user_version = %d after reopen, want 4", uv)
	}
}

func TestMigration0003ConfigsRoles(t *testing.T) {
	s := newTestStore(t) // Open() runs every migration, incl. 0003

	// user_version reached 3 (three migrations applied).
	var uv int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if uv != 4 {
		t.Fatalf("user_version = %d, want 4", uv)
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
