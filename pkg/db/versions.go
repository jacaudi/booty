package db

import "fmt"

// TargetVersion is one version row attached to a target.
type TargetVersion struct {
	ID       int64
	TargetID int64
	Version  string
	Source   string // 'discovered' | 'manual'
	Cached   bool
}

// UpsertTargetVersion inserts tv, or updates source/cached if (target_id,version)
// already exists.
func (s *Store) UpsertTargetVersion(tv TargetVersion) error {
	_, err := s.db.Exec(
		`INSERT INTO target_versions (target_id, version, source, cached)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(target_id, version) DO UPDATE SET
		   source = excluded.source,
		   cached = excluded.cached`,
		tv.TargetID, tv.Version, tv.Source, tv.Cached,
	)
	if err != nil {
		return fmt.Errorf("db: upsert version %d/%s: %w", tv.TargetID, tv.Version, err)
	}
	return nil
}

// DeleteTargetVersion removes the (targetID, version) row. Idempotent: deleting
// an absent row is a no-op returning nil. The caller is responsible for removing
// the on-disk artifacts.
func (s *Store) DeleteTargetVersion(targetID int64, version string) error {
	if _, err := s.db.Exec(
		`DELETE FROM target_versions WHERE target_id = ? AND version = ?`, targetID, version); err != nil {
		return fmt.Errorf("db: delete version %d/%s: %w", targetID, version, err)
	}
	return nil
}

// TargetVersionID returns the row id for (targetID, version), or an error
// wrapping sql.ErrNoRows if the row does not exist.
func (s *Store) TargetVersionID(targetID int64, version string) (int64, error) {
	var id int64
	err := s.db.QueryRow(
		`SELECT id FROM target_versions WHERE target_id = ? AND version = ?`, targetID, version).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("db: target_version id %d/%s: %w", targetID, version, err)
	}
	return id, nil
}

// ListTargetVersions returns the versions for targetID ordered by id.
func (s *Store) ListTargetVersions(targetID int64) ([]TargetVersion, error) {
	rows, err := s.db.Query(
		`SELECT id, target_id, version, source, cached
		   FROM target_versions WHERE target_id = ? ORDER BY id`, targetID)
	if err != nil {
		return nil, fmt.Errorf("db: list versions for %d: %w", targetID, err)
	}
	defer rows.Close()

	var out []TargetVersion
	for rows.Next() {
		var tv TargetVersion
		if err := rows.Scan(&tv.ID, &tv.TargetID, &tv.Version, &tv.Source, &tv.Cached); err != nil {
			return nil, fmt.Errorf("db: scan version: %w", err)
		}
		out = append(out, tv)
	}
	return out, rows.Err()
}
