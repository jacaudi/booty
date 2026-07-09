package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrate applies every embedded migration whose 1-based ordinal (by sorted
// filename) exceeds the database's current PRAGMA user_version. Each migration
// runs in its own transaction and bumps user_version on commit; any error
// aborts immediately (fail fast) with prior migrations already committed.
//
// When at least one migration is applied, the loop runs on ONE dedicated
// connection with foreign-key enforcement OFF, per SQLite's documented
// table-rebuild procedure (lang_altertable §7): under foreign_keys=ON, a
// rebuild's DROP TABLE performs an implicit DELETE that fires ON DELETE
// actions — 0004's configs rebuild would cascade-wipe config_revisions. The
// pragma cannot live in the migration file itself (it is a no-op inside the
// per-migration transaction), so it brackets the loop here; foreign_key_check
// then verifies no rebuild left dangling references, and the pragma is
// restored before the connection returns to the pool (MaxOpenConns is 1 — a
// stuck-OFF connection would BE the store's connection). On error the caller
// (Open) closes the whole handle, so a stuck-OFF connection cannot leak.
//
// A NO-MIGRATION reopen (the common steady-state startup, current == latest)
// touches none of that: it applies nothing, so it toggles no pragma and runs
// no foreign_key_check — byte-identical to the pre-P5 runner, and it does NOT
// newly fail-close a database that happens to carry a pre-existing dangling
// FK. The FK bracket + check are a cost paid ONLY on a real schema change.
func (s *Store) migrate() error {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migration connection: %w", err)
	}
	defer conn.Close()

	var current int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	// Nothing to apply → the whole FK OFF/check/ON bracket is skipped, so a
	// steady-state reopen behaves exactly as the pre-P5 runner did.
	latest := len(entries)
	if current >= latest {
		return nil
	}

	// A migration will run: disable FK enforcement for the rebuild-safe window.
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}

	for i, e := range entries {
		version := i + 1
		if version <= current {
			continue
		}
		name := e.Name()
		stmt, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin %s: %w", name, err)
		}
		if _, err := tx.Exec(string(stmt)); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec %s: %w", name, err)
		}
		// PRAGMA cannot take a bind parameter; version is a code-controlled int,
		// so the formatted value is not an injection vector.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			tx.Rollback()
			return fmt.Errorf("set user_version %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}
	}

	// A rebuild must never orphan child rows — fail fast if one did. This runs
	// only because we applied ≥1 migration (guarded by the early return above).
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	dangling := rows.Next()
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return fmt.Errorf("foreign_key_check: %w", rowsErr)
	}
	if dangling {
		return errors.New("migration left dangling foreign-key references")
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("re-enable foreign_keys: %w", err)
	}
	return nil
}
