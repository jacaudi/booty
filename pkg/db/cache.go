package db

import (
	"errors"
	"fmt"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("db: not found")

// CacheEntryRow is a cache_entries row joined to its target_version/target, for
// listing. State is derived from (InWindow, Pinned) by callers.
type CacheEntryRow struct {
	ID              int64
	TargetVersionID int64
	OS              string
	Arch            string
	Params          string // canonical JSON, as stored on targets
	Version         string
	Size            int64
	FetchedAt       string
	InWindow        bool
	Pinned          bool
}

// CacheFilter filters ListCacheEntries. Empty fields mean "no filter".
type CacheFilter struct {
	OS       string
	Arch     string
	Pinned   *bool
	InWindow *bool
}

const cacheEntryJoin = `
	SELECT ce.id, ce.target_version_id, t.os, t.arch, t.params, tv.version,
	       ce.size, ce.fetched_at, ce.in_window, ce.pinned
	  FROM cache_entries ce
	  JOIN target_versions tv ON tv.id = ce.target_version_id
	  JOIN targets t          ON t.id  = tv.target_id`

// UpsertCacheEntry inserts (or updates) the cache_entries row for a
// target_version, setting size/fetched_at/in_window=1. It NEVER clobbers pinned
// (an operator pin survives re-caching) nor verified/verify_err (P3b owns them).
func (s *Store) UpsertCacheEntry(targetVersionID, size int64) error {
	_, err := s.db.Exec(
		`INSERT INTO cache_entries (target_version_id, size, in_window)
		 VALUES (?, ?, 1)
		 ON CONFLICT(target_version_id) DO UPDATE SET
		   size       = excluded.size,
		   fetched_at = datetime('now'),
		   in_window  = 1`,
		targetVersionID, size,
	)
	if err != nil {
		return fmt.Errorf("db: upsert cache_entry tv=%d: %w", targetVersionID, err)
	}
	return nil
}

// SetCacheInWindow flips a cache_entries row's in_window (archived when false).
// No-op if the row is absent.
func (s *Store) SetCacheInWindow(targetVersionID int64, inWindow bool) error {
	if _, err := s.db.Exec(
		`UPDATE cache_entries SET in_window = ? WHERE target_version_id = ?`,
		boolToInt(inWindow), targetVersionID); err != nil {
		return fmt.Errorf("db: set in_window tv=%d: %w", targetVersionID, err)
	}
	return nil
}

// SetCachePinned sets pinned by cache_entries.id.
func (s *Store) SetCachePinned(id int64, pinned bool) error {
	if _, err := s.db.Exec(
		`UPDATE cache_entries SET pinned = ? WHERE id = ?`, boolToInt(pinned), id); err != nil {
		return fmt.Errorf("db: set pinned id=%d: %w", id, err)
	}
	return nil
}

func (s *Store) ListCacheEntries(f CacheFilter) ([]CacheEntryRow, error) {
	q := cacheEntryJoin + " WHERE 1=1"
	var args []any
	if f.OS != "" {
		q += " AND t.os = ?"
		args = append(args, f.OS)
	}
	if f.Arch != "" {
		q += " AND t.arch = ?"
		args = append(args, f.Arch)
	}
	if f.Pinned != nil {
		q += " AND ce.pinned = ?"
		args = append(args, boolToInt(*f.Pinned))
	}
	if f.InWindow != nil {
		q += " AND ce.in_window = ?"
		args = append(args, boolToInt(*f.InWindow))
	}
	q += " ORDER BY t.os, t.arch, tv.version"
	return s.queryCacheRows(q, args...)
}

// GetCacheEntry returns one joined row by cache_entries.id, or ErrNotFound.
func (s *Store) GetCacheEntry(id int64) (CacheEntryRow, error) {
	rows, err := s.queryCacheRows(cacheEntryJoin+" WHERE ce.id = ?", id)
	if err != nil {
		return CacheEntryRow{}, err
	}
	if len(rows) == 0 {
		return CacheEntryRow{}, ErrNotFound
	}
	return rows[0], nil
}

// SumCacheBytes totals size across all cache_entries.
func (s *Store) SumCacheBytes() (int64, error) {
	var n int64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM cache_entries`).Scan(&n); err != nil {
		return 0, fmt.Errorf("db: sum cache bytes: %w", err)
	}
	return n, nil
}

// ListArchivedUnpinned returns archived (in_window=0), unpinned rows, oldest
// fetched_at first — the eviction candidate order.
func (s *Store) ListArchivedUnpinned() ([]CacheEntryRow, error) {
	return s.queryCacheRows(cacheEntryJoin +
		" WHERE ce.in_window = 0 AND ce.pinned = 0 ORDER BY ce.fetched_at ASC, ce.id ASC")
}

func (s *Store) queryCacheRows(q string, args ...any) ([]CacheEntryRow, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: query cache_entries: %w", err)
	}
	defer rows.Close()
	var out []CacheEntryRow
	for rows.Next() {
		var r CacheEntryRow
		var inWin, pinned int
		if err := rows.Scan(&r.ID, &r.TargetVersionID, &r.OS, &r.Arch, &r.Params,
			&r.Version, &r.Size, &r.FetchedAt, &inWin, &pinned); err != nil {
			return nil, fmt.Errorf("db: scan cache_entry: %w", err)
		}
		r.InWindow, r.Pinned = inWin == 1, pinned == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
