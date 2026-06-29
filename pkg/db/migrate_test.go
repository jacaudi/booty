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

	for _, table := range []string{"targets", "target_versions", "meta", "hosts"} {
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
	if uv != 1 {
		t.Errorf("user_version = %d, want 1 after one migration", uv)
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
	if uv != 1 {
		t.Errorf("user_version = %d after reopen, want 1", uv)
	}
}
