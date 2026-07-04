package db

import (
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
	if uv != 3 {
		t.Errorf("user_version = %d, want 3 after all migrations", uv)
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
	if uv != 3 {
		t.Errorf("user_version = %d after reopen, want 3", uv)
	}
}

func TestMigration0003ConfigsRoles(t *testing.T) {
	s := newTestStore(t) // Open() runs every migration, incl. 0003

	// user_version reached 3 (three migrations applied).
	var uv int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if uv != 3 {
		t.Fatalf("user_version = %d, want 3", uv)
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
