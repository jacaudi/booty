package db

import (
	"cmp"
	"database/sql"
	"fmt"
)

// Target is one operator-declared cache target. Params is the canonical JSON
// encoding of the per-OS path-discriminating params (e.g. {"schematic":"..."}).
type Target struct {
	ID      int64
	OS      string
	Arch    string
	Params  string
	Mode    string // 'discovery' | 'manual'
	RetainN int
	Source  string // 'catalog' | 'api' | 'host'
	Enabled bool

	// SourceMode, DvdCount, and DesiredMode are mutable serving-mode state, not
	// part of target identity. CreateTarget/UpsertTarget persist them on INSERT
	// (zero values fall back to the netinst/1/NULL defaults via cmp.Or), but the
	// upsert's ON CONFLICT branch never touches them — after creation they are
	// owned by SetTargetDesiredMode/SetTargetSourceMode (and the reconciler).
	SourceMode  string // 'netinst' | 'dvd'; zero value persists as 'netinst'
	DvdCount    int    // zero value persists as 1
	DesiredMode string // pending promote intent; '' <=> SQL NULL (no pending promote)
}

// UpsertTarget inserts t, or updates mode/retain_n/source/enabled if
// (os,arch,params) already exists. Used for idempotent predefined-target
// seeding (re-run every tick). Params MUST be the canonical encoding so equal
// param sets collide on the UNIQUE(os,arch,params) constraint.
//
// The INSERT branch persists source_mode/dvd_count/desired_mode (zero values
// fall back to netinst/1/NULL via cmp.Or), so a first-time seed can declare a
// DVD target. The ON CONFLICT DO UPDATE branch deliberately does NOT touch
// those columns: they are reconciler/promote-endpoint-owned mutable state (see
// Target), and updating them on a re-run would revert an operator-promoted DVD
// target back to t's values every tick. After creation, only
// SetTargetDesiredMode/SetTargetSourceMode write them.
func (s *Store) UpsertTarget(t Target) error {
	_, err := s.db.Exec(
		`INSERT INTO targets (os, arch, params, mode, retain_n, source, enabled, source_mode, dvd_count, desired_mode)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(os, arch, params) DO UPDATE SET
		   mode       = excluded.mode,
		   retain_n   = excluded.retain_n,
		   source     = excluded.source,
		   enabled    = excluded.enabled,
		   updated_at = datetime('now')`,
		t.OS, t.Arch, t.Params, t.Mode, t.RetainN, t.Source, t.Enabled,
		cmp.Or(t.SourceMode, "netinst"), cmp.Or(t.DvdCount, 1),
		sql.NullString{String: t.DesiredMode, Valid: t.DesiredMode != ""},
	)
	if err != nil {
		return fmt.Errorf("db: upsert target %s/%s: %w", t.OS, t.Arch, err)
	}
	return nil
}

// EnsureTarget inserts t only if no (os,arch,params) row exists; an existing
// row is left completely untouched (ON CONFLICT DO NOTHING). This is the
// create-if-absent seeding primitive (#48 D1): flags preseed a predefined row
// on first boot, then the API owns mode/retain_n/enabled. Params MUST be the
// canonical encoding so equal param sets collide on UNIQUE(os,arch,params).
func (s *Store) EnsureTarget(t Target) error {
	_, err := s.db.Exec(
		`INSERT INTO targets (os, arch, params, mode, retain_n, source, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(os, arch, params) DO NOTHING`,
		t.OS, t.Arch, t.Params, t.Mode, t.RetainN, t.Source, t.Enabled,
	)
	if err != nil {
		return fmt.Errorf("db: ensure target %s/%s: %w", t.OS, t.Arch, err)
	}
	return nil
}

// CreateTarget inserts t and returns its new id. A duplicate (os,arch,params)
// violates the UNIQUE constraint and returns an error.
//
// source_mode/dvd_count/desired_mode are persisted so a caller (e.g. the
// catalog CREATE path) can declare a DVD target at creation. Zero values fall
// back to the schema defaults via cmp.Or (source_mode 'netinst', dvd_count 1),
// which keeps existing callers that never set them valid against the
// source_mode CHECK; desired_mode's empty string maps to SQL NULL.
func (s *Store) CreateTarget(t Target) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO targets (os, arch, params, mode, retain_n, source, enabled, source_mode, dvd_count, desired_mode)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.OS, t.Arch, t.Params, t.Mode, t.RetainN, t.Source, t.Enabled,
		cmp.Or(t.SourceMode, "netinst"), cmp.Or(t.DvdCount, 1),
		sql.NullString{String: t.DesiredMode, Valid: t.DesiredMode != ""},
	)
	if err != nil {
		return 0, fmt.Errorf("db: create target %s/%s: %w", t.OS, t.Arch, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("db: create target id: %w", err)
	}
	return id, nil
}

// UpdateTargetParams rewrites a target's params IN PLACE by row id, preserving
// its target_versions/cache_entries (row identity is unchanged). Used only by
// the one-time #48 channel migration. The caller must pass the canonical
// encoding and a path-safe value.
func (s *Store) UpdateTargetParams(id int64, params string) error {
	if _, err := s.db.Exec(
		`UPDATE targets SET params = ?, updated_at = datetime('now') WHERE id = ?`,
		params, id); err != nil {
		return fmt.Errorf("db: update target params id=%d: %w", id, err)
	}
	return nil
}

// UpdateTargetFromCatalog reconciles a target's catalog-declared fields
// (enabled, retain_n) and marks it source='catalog', preserving mode and
// identity. Used by the catalog-apply pass (create-if-absent handles new rows).
func (s *Store) UpdateTargetFromCatalog(id int64, enabled bool, retainN int) error {
	if _, err := s.db.Exec(
		`UPDATE targets SET source = 'catalog', enabled = ?, retain_n = ?, updated_at = datetime('now') WHERE id = ?`,
		enabled, retainN, id); err != nil {
		return fmt.Errorf("db: update target from catalog id=%d: %w", id, err)
	}
	return nil
}

// DisableTarget sets enabled=false, preserving everything else. Used for
// catalog-removed source='catalog' rows (bytes are kept; eviction reclaims).
func (s *Store) DisableTarget(id int64) error {
	if _, err := s.db.Exec(
		`UPDATE targets SET enabled = 0, updated_at = datetime('now') WHERE id = ?`, id); err != nil {
		return fmt.Errorf("db: disable target id=%d: %w", id, err)
	}
	return nil
}

// GetTarget returns the target with id, or sql.ErrNoRows if none.
func (s *Store) GetTarget(id int64) (*Target, error) {
	var t Target
	var desired sql.NullString
	err := s.db.QueryRow(
		`SELECT id, os, arch, params, mode, retain_n, source, enabled, source_mode, dvd_count, desired_mode
		   FROM targets WHERE id = ?`, id,
	).Scan(&t.ID, &t.OS, &t.Arch, &t.Params, &t.Mode, &t.RetainN, &t.Source, &t.Enabled,
		&t.SourceMode, &t.DvdCount, &desired)
	if err != nil {
		return nil, err
	}
	t.DesiredMode = desired.String
	return &t, nil
}

// ListTargets returns all targets ordered by id.
func (s *Store) ListTargets() ([]Target, error) {
	rows, err := s.db.Query(
		`SELECT id, os, arch, params, mode, retain_n, source, enabled, source_mode, dvd_count, desired_mode
		   FROM targets ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("db: list targets: %w", err)
	}
	defer rows.Close()

	var out []Target
	for rows.Next() {
		var t Target
		var desired sql.NullString
		if err := rows.Scan(&t.ID, &t.OS, &t.Arch, &t.Params, &t.Mode, &t.RetainN, &t.Source, &t.Enabled,
			&t.SourceMode, &t.DvdCount, &desired); err != nil {
			return nil, fmt.Errorf("db: scan target: %w", err)
		}
		t.DesiredMode = desired.String
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetTargetDesiredMode records a promote intent (design §8.5). It does not touch
// the effective source_mode; the reconciler flips that on success.
func (s *Store) SetTargetDesiredMode(id int64, mode string, dvdCount int) error {
	if _, err := s.db.Exec(
		`UPDATE targets SET desired_mode=?, dvd_count=?, updated_at=datetime('now') WHERE id=?`,
		mode, dvdCount, id); err != nil {
		return fmt.Errorf("db: set desired_mode id=%d: %w", id, err)
	}
	return nil
}

// SetTargetSourceMode flips the effective serving mode and clears any pending
// promote intent. Called by the reconciler after the DVD tree lands.
func (s *Store) SetTargetSourceMode(id int64, mode string) error {
	if _, err := s.db.Exec(
		`UPDATE targets SET source_mode=?, desired_mode=NULL, updated_at=datetime('now') WHERE id=?`,
		mode, id); err != nil {
		return fmt.Errorf("db: set source_mode id=%d: %w", id, err)
	}
	return nil
}

// GetTargetByIdentity returns the target with the given (os,arch,params) tuple,
// or sql.ErrNoRows. params MUST be the canonical encoding. Used by the boot and
// preseed paths to resolve a Debian host's source_mode.
func (s *Store) GetTargetByIdentity(os, arch, params string) (*Target, error) {
	var t Target
	var desired sql.NullString
	err := s.db.QueryRow(
		`SELECT id, os, arch, params, mode, retain_n, source, enabled, source_mode, dvd_count, desired_mode
		   FROM targets WHERE os=? AND arch=? AND params=?`, os, arch, params,
	).Scan(&t.ID, &t.OS, &t.Arch, &t.Params, &t.Mode, &t.RetainN, &t.Source, &t.Enabled,
		&t.SourceMode, &t.DvdCount, &desired)
	if err != nil {
		return nil, err
	}
	t.DesiredMode = desired.String
	return &t, nil
}
