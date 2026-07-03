package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// Config is the logical config identity; the live source lives in the revision
// pointed at by ActiveRevisionID.
type Config struct {
	ID               int64
	Name             string
	Kind             string // 'butane' | 'machineconfig' | 'preseed'
	ActiveRevisionID sql.NullInt64
	CreatedAt        string
	UpdatedAt        string
}

// ConfigRevision is an immutable, append-only full copy of a config's source.
type ConfigRevision struct {
	ID        int64
	ConfigID  int64
	Revision  int
	SourceB64 string
	SHA256    string
	CreatedAt string
}

// ConfigListRow is the list projection with the computed active-revision number
// and revision count (one query, subquery-derived) for the API DTO.
type ConfigListRow struct {
	ID             int64
	Name           string
	Kind           string
	ActiveRevision int // 0 when no active revision
	RevisionCount  int
	UpdatedAt      string
}

// CreateConfig inserts a config identity (no revision yet) and returns its id.
// A duplicate name violates UNIQUE and returns an error.
func (s *Store) CreateConfig(name, kind string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO configs (name, kind) VALUES (?, ?)`, name, kind)
	if err != nil {
		return 0, fmt.Errorf("db: create config %q: %w", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("db: create config id: %w", err)
	}
	return id, nil
}

// GetConfig returns the config identity, or ErrNotFound.
func (s *Store) GetConfig(id int64) (*Config, error) {
	var c Config
	err := s.db.QueryRow(
		`SELECT id, name, kind, active_revision_id, created_at, updated_at
		   FROM configs WHERE id = ?`, id,
	).Scan(&c.ID, &c.Name, &c.Kind, &c.ActiveRevisionID, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get config %d: %w", id, err)
	}
	return &c, nil
}

// ListConfigs returns every config with its active-revision number and revision
// count, ordered by name.
func (s *Store) ListConfigs() ([]ConfigListRow, error) {
	rows, err := s.db.Query(
		`SELECT c.id, c.name, c.kind,
		        COALESCE(ar.revision, 0),
		        (SELECT COUNT(*) FROM config_revisions r WHERE r.config_id = c.id),
		        c.updated_at
		   FROM configs c
		   LEFT JOIN config_revisions ar ON ar.id = c.active_revision_id
		  ORDER BY c.name`)
	if err != nil {
		return nil, fmt.Errorf("db: list configs: %w", err)
	}
	defer rows.Close()
	var out []ConfigListRow
	for rows.Next() {
		var r ConfigListRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.ActiveRevision, &r.RevisionCount, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("db: scan config: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddConfigRevision appends an immutable revision (revision = max+1) and returns
// its row id and revision number. It does NOT change the active pointer; the
// caller advances active via SetActiveRevision.
func (s *Store) AddConfigRevision(configID int64, sourceB64, sha256 string) (int64, int, error) {
	var next int
	if err := s.db.QueryRow(
		`SELECT COALESCE(MAX(revision), 0) + 1 FROM config_revisions WHERE config_id = ?`,
		configID).Scan(&next); err != nil {
		return 0, 0, fmt.Errorf("db: next revision for %d: %w", configID, err)
	}
	res, err := s.db.Exec(
		`INSERT INTO config_revisions (config_id, revision, source_b64, source_sha256)
		 VALUES (?, ?, ?, ?)`, configID, next, sourceB64, sha256)
	if err != nil {
		return 0, 0, fmt.Errorf("db: add revision %d: %w", configID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, 0, fmt.Errorf("db: add revision id: %w", err)
	}
	return id, next, nil
}

// SetActiveRevision repoints a config's active revision (used by edit AND
// rollback — rollback passes an existing older revision's id). Touches
// updated_at. No content is copied.
func (s *Store) SetActiveRevision(configID, revisionID int64) error {
	if _, err := s.db.Exec(
		`UPDATE configs SET active_revision_id = ?, updated_at = datetime('now') WHERE id = ?`,
		revisionID, configID); err != nil {
		return fmt.Errorf("db: set active revision %d=%d: %w", configID, revisionID, err)
	}
	return nil
}

// GetActiveRevision returns the config's live revision, or ErrNotFound when the
// config has no active revision (or does not exist).
func (s *Store) GetActiveRevision(configID int64) (*ConfigRevision, error) {
	var r ConfigRevision
	err := s.db.QueryRow(
		`SELECT r.id, r.config_id, r.revision, r.source_b64, r.source_sha256, r.created_at
		   FROM config_revisions r
		   JOIN configs c ON c.active_revision_id = r.id
		  WHERE c.id = ?`, configID,
	).Scan(&r.ID, &r.ConfigID, &r.Revision, &r.SourceB64, &r.SHA256, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: active revision %d: %w", configID, err)
	}
	return &r, nil
}

// GetRevision returns a config's revision by its per-config number, or ErrNotFound.
func (s *Store) GetRevision(configID int64, revision int) (*ConfigRevision, error) {
	var r ConfigRevision
	err := s.db.QueryRow(
		`SELECT id, config_id, revision, source_b64, source_sha256, created_at
		   FROM config_revisions WHERE config_id = ? AND revision = ?`, configID, revision,
	).Scan(&r.ID, &r.ConfigID, &r.Revision, &r.SourceB64, &r.SHA256, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: revision %d/%d: %w", configID, revision, err)
	}
	return &r, nil
}

// ListRevisions returns a config's revisions, newest first.
func (s *Store) ListRevisions(configID int64) ([]ConfigRevision, error) {
	rows, err := s.db.Query(
		`SELECT id, config_id, revision, source_b64, source_sha256, created_at
		   FROM config_revisions WHERE config_id = ? ORDER BY revision DESC`, configID)
	if err != nil {
		return nil, fmt.Errorf("db: list revisions %d: %w", configID, err)
	}
	defer rows.Close()
	var out []ConfigRevision
	for rows.Next() {
		var r ConfigRevision
		if err := rows.Scan(&r.ID, &r.ConfigID, &r.Revision, &r.SourceB64, &r.SHA256, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("db: scan revision: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountRevisions returns how many revisions a config has.
func (s *Store) CountRevisions(configID int64) (int, error) {
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM config_revisions WHERE config_id = ?`, configID).Scan(&n); err != nil {
		return 0, fmt.Errorf("db: count revisions %d: %w", configID, err)
	}
	return n, nil
}

// PruneRevisions keeps the newest `keep` revisions by revision number UNION the
// currently-active revision, deleting the rest. The active row is ALWAYS
// protected (a rollback-to-old-rev then edit must never evict the live config).
// keep <= 0 is treated as 1.
func (s *Store) PruneRevisions(configID int64, keep int) error {
	if keep < 1 {
		keep = 1
	}
	_, err := s.db.Exec(
		`DELETE FROM config_revisions
		  WHERE config_id = ?
		    AND id NOT IN (
		        SELECT id FROM config_revisions
		         WHERE config_id = ? ORDER BY revision DESC LIMIT ?)
		    AND id NOT IN (
		        SELECT active_revision_id FROM configs WHERE id = ? AND active_revision_id IS NOT NULL)`,
		configID, configID, keep, configID)
	if err != nil {
		return fmt.Errorf("db: prune revisions %d: %w", configID, err)
	}
	return nil
}
