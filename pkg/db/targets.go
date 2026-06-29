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
