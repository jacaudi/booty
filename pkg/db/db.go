// Package db is booty's thin, typed wrapper over a pure-Go SQLite database
// (modernc.org/sqlite — CGO-free, so the distroless static build keeps working).
// It is the authoritative store for control-plane and host state.
package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Store wraps a SQLite handle. It is safe for concurrent use; writes are
// serialized by SetMaxOpenConns(1) and SQLite's busy timeout.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path, applies the
// per-connection pragmas (WAL journaling, foreign keys, 5s busy timeout) via
// the DSN, verifies connectivity, and runs all embedded migrations. It fails
// fast: any pragma, connectivity, or migration error returns an error and no
// usable Store. A single open connection serializes writes, which removes
// SQLITE_BUSY contention for booty's low write volume.
func Open(path string) (*Store, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	sqldb.SetMaxOpenConns(1)
	if err := sqldb.Ping(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("db: ping %s: %w", path, err)
	}
	s := &Store{db: sqldb}
	if err := s.migrate(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("db: migrate %s: %w", path, err)
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

