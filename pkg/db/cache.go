package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
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
	Verified        *bool  // NULL = no verdict; true/false = P3b verification result
	VerifyErr       string // errors.Join of failing artifacts' messages ("" when none)
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
	       ce.size, ce.fetched_at, ce.in_window, ce.pinned, ce.verified, ce.verify_err
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

// SetCacheVerified records a version's verification verdict on its cache_entries
// row. A nil verified clears the column to NULL ("no verdict") — required when a
// reverify finds zero verifiable artifacts (e.g. an FCOS pattern-fallback pin).
// It touches only verified/verify_err; size/in_window/pinned are unchanged.
// No-op if the row is absent.
func (s *Store) SetCacheVerified(targetVersionID int64, verified *bool, verifyErr string) error {
	var v any
	if verified != nil {
		v = boolToInt(*verified)
	}
	if _, err := s.db.Exec(
		`UPDATE cache_entries SET verified = ?, verify_err = ? WHERE target_version_id = ?`,
		v, verifyErr, targetVersionID); err != nil {
		return fmt.Errorf("db: set verified tv=%d: %w", targetVersionID, err)
	}
	return nil
}

// UpsertCacheEntryArchived writes the failure-visibility row for a version that
// was REJECTED (bytes never landed / were removed): size=0, in_window=0,
// verified=0 with the verify_err text, so the Cache view shows an archived,
// failed row with the error tooltip instead of silence. size=0 keeps it out of
// the eviction candidate set and the byte budget (D14). It never clobbers pinned.
func (s *Store) UpsertCacheEntryArchived(targetVersionID int64, verifyErr string) error {
	_, err := s.db.Exec(
		`INSERT INTO cache_entries (target_version_id, size, in_window, verified, verify_err)
		 VALUES (?, 0, 0, 0, ?)
		 ON CONFLICT(target_version_id) DO UPDATE SET
		   size       = 0,
		   fetched_at = datetime('now'),
		   in_window  = 0,
		   verified   = 0,
		   verify_err = excluded.verify_err`,
		targetVersionID, verifyErr,
	)
	if err != nil {
		return fmt.Errorf("db: upsert archived cache_entry tv=%d: %w", targetVersionID, err)
	}
	return nil
}

// VerifyRejectedWithin reports whether (targetID, version) currently carries a
// VERIFICATION-REJECTION row newer than `within`, and returns that row's
// verify_err. blocked=false covers both "no row" and "row does not match",
// which is the whole answer the caller needs — there is no third state.
//
// The predicate is ALL FOUR columns (size=0, in_window=0, verified=0,
// verify_err<>''), which is the only combination UpsertCacheEntryArchived
// produces. Every other writer was checked: UpsertCacheEntry unconditionally
// sets in_window=1, SetCacheInWindow is only ever called with false and touches
// nothing else, and SetCachePinned* touch only pinned. The looser two-column
// predicate (cached=0 plus a failure verdict) misfires on a reachable sequence:
// a warn-landed failure that later loses a file on disk drops cached to 0 via
// versions.go's `cached = excluded.cached` while keeping a STALE verified=0 and
// verify_err, and a subsequent transport error writes nothing — forging that
// signature after exactly the kind of failure this guard must never suppress.
//
// One residual, reported rather than hidden: SetCacheVerified can stamp
// verified=0/verify_err onto a row already sitting at size=0/in_window=0, so
// "only writer" is strictly too strong. Neither it nor SetCacheInWindow touches
// fetched_at, so the recency clause bounds the exposure to one guarded window on
// a version whose bytes are already gone.
//
// THE COMPARISON HAPPENS IN SQL, NEVER IN GO. cache_entries.fetched_at is a UTC
// datetime() TEXT string that nothing in this repo parses, and the driver has no
// time-parsing DSN option; parsing it with time.ParseInLocation(..., time.Local)
// yields a FUTURE timestamp and wedges the version for hours.
//
// The modifier MUST be spelled "-N seconds". SQLite rejects a Go Duration string:
// datetime('now','-1h0m0s') returns NULL, `fetched_at > NULL` is NULL, the WHERE
// never matches, and this function silently becomes a no-op with no error, no
// log, and no failing test except the in-window one. A zero window yields
// "-0 seconds" — valid, and never matches, so the guard is disabled. A NEGATIVE
// window yields "--3600 seconds", which SQLite rejects to NULL and therefore
// also never blocks; both degenerate cases fail safe, by different routes.
func (s *Store) VerifyRejectedWithin(targetID int64, version string, within time.Duration) (bool, string, error) {
	modifier := fmt.Sprintf("-%d seconds", int64(within/time.Second))
	var verifyErr string
	err := s.db.QueryRow(`
		SELECT ce.verify_err FROM cache_entries ce
		  JOIN target_versions tv ON tv.id = ce.target_version_id
		 WHERE tv.target_id = ? AND tv.version = ?
		   AND ce.size = 0 AND ce.in_window = 0 AND ce.verified = 0 AND ce.verify_err <> ''
		   AND ce.fetched_at > datetime('now', ?)`,
		targetID, version, modifier).Scan(&verifyErr)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("db: verify-rejected guard %d/%s: %w", targetID, version, err)
	}
	return true, verifyErr, nil
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

// SetCachePinnedByTargetVersion pins by target_version_id (SetCachePinned
// keys on cache_entries.id, which the Debian DVD reconciler branch does not
// hold — it only has the target_version_id it just upserted).
func (s *Store) SetCachePinnedByTargetVersion(tvID int64, pinned bool) error {
	if _, err := s.db.Exec(
		`UPDATE cache_entries SET pinned = ? WHERE target_version_id = ?`,
		boolToInt(pinned), tvID); err != nil {
		return fmt.Errorf("db: pin tv=%d: %w", tvID, err)
	}
	return nil
}

// CacheEntryExists reports whether a cache_entries row exists for
// targetVersionID — used by the Debian DVD reconciler's fully-settled
// short-circuit to distinguish "sentinel present + rows recorded" (true
// no-op) from "sentinel present + rows missing" (self-heal still required).
func (s *Store) CacheEntryExists(targetVersionID int64) (bool, error) {
	var exists bool
	if err := s.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM cache_entries WHERE target_version_id = ?)`, targetVersionID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("db: cache entry exists tv=%d: %w", targetVersionID, err)
	}
	return exists, nil
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

// ListArchivedUnpinned returns archived (in_window=0), unpinned, NON-EMPTY
// (size>0) rows, oldest fetched_at first — the eviction candidate order. size=0
// failure-visibility rows are excluded (D14): they free no bytes, so evicting
// them would stall the no-progress guard while real archived bytes go unreclaimed.
func (s *Store) ListArchivedUnpinned() ([]CacheEntryRow, error) {
	return s.queryCacheRows(cacheEntryJoin +
		" WHERE ce.in_window = 0 AND ce.pinned = 0 AND ce.size > 0 ORDER BY ce.fetched_at ASC, ce.id ASC")
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
		var verified sql.NullInt64
		var verifyErr sql.NullString
		if err := rows.Scan(&r.ID, &r.TargetVersionID, &r.OS, &r.Arch, &r.Params,
			&r.Version, &r.Size, &r.FetchedAt, &inWin, &pinned, &verified, &verifyErr); err != nil {
			return nil, fmt.Errorf("db: scan cache_entry: %w", err)
		}
		r.InWindow, r.Pinned = inWin == 1, pinned == 1
		if verified.Valid {
			b := verified.Int64 == 1
			r.Verified = &b
		}
		r.VerifyErr = verifyErr.String
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
