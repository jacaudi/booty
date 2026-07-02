package db

import "fmt"

// Target is one operator-declared cache target. Params is the canonical JSON
// encoding of the per-OS path-discriminating params (e.g. {"schematic":"..."}).
type Target struct {
	ID         int64
	OS         string
	Arch       string
	Params     string
	Mode       string // 'discovery' | 'manual'
	RetainN    int
	Predefined bool
	Enabled    bool
}

// UpsertTarget inserts t, or updates mode/retain_n/predefined/enabled if
// (os,arch,params) already exists. Used for idempotent predefined-target
// seeding (re-run every tick). Params MUST be the canonical encoding so equal
// param sets collide on the UNIQUE(os,arch,params) constraint.
func (s *Store) UpsertTarget(t Target) error {
	_, err := s.db.Exec(
		`INSERT INTO targets (os, arch, params, mode, retain_n, predefined, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(os, arch, params) DO UPDATE SET
		   mode       = excluded.mode,
		   retain_n   = excluded.retain_n,
		   predefined = excluded.predefined,
		   enabled    = excluded.enabled,
		   updated_at = datetime('now')`,
		t.OS, t.Arch, t.Params, t.Mode, t.RetainN, t.Predefined, t.Enabled,
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
		`INSERT INTO targets (os, arch, params, mode, retain_n, predefined, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(os, arch, params) DO NOTHING`,
		t.OS, t.Arch, t.Params, t.Mode, t.RetainN, t.Predefined, t.Enabled,
	)
	if err != nil {
		return fmt.Errorf("db: ensure target %s/%s: %w", t.OS, t.Arch, err)
	}
	return nil
}

// CreateTarget inserts t and returns its new id. A duplicate (os,arch,params)
// violates the UNIQUE constraint and returns an error.
func (s *Store) CreateTarget(t Target) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO targets (os, arch, params, mode, retain_n, predefined, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.OS, t.Arch, t.Params, t.Mode, t.RetainN, t.Predefined, t.Enabled,
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

// GetTarget returns the target with id, or sql.ErrNoRows if none.
func (s *Store) GetTarget(id int64) (*Target, error) {
	var t Target
	err := s.db.QueryRow(
		`SELECT id, os, arch, params, mode, retain_n, predefined, enabled
		   FROM targets WHERE id = ?`, id,
	).Scan(&t.ID, &t.OS, &t.Arch, &t.Params, &t.Mode, &t.RetainN, &t.Predefined, &t.Enabled)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListTargets returns all targets ordered by id.
func (s *Store) ListTargets() ([]Target, error) {
	rows, err := s.db.Query(
		`SELECT id, os, arch, params, mode, retain_n, predefined, enabled
		   FROM targets ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("db: list targets: %w", err)
	}
	defer rows.Close()

	var out []Target
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.ID, &t.OS, &t.Arch, &t.Params, &t.Mode, &t.RetainN, &t.Predefined, &t.Enabled); err != nil {
			return nil, fmt.Errorf("db: scan target: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
