package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// Role is a fleet grouping with an optional fleet-wide default config.
type Role struct {
	ID              int64
	Name            string
	DefaultConfigID sql.NullInt64
	CreatedAt       string
	UpdatedAt       string
}

// RoleListRow is the list projection with the bound-host count for the API DTO.
type RoleListRow struct {
	ID              int64
	Name            string
	DefaultConfigID sql.NullInt64
	HostCount       int
}

// CreateRole inserts a role and returns its id. defaultConfigID may be nil.
func (s *Store) CreateRole(name string, defaultConfigID *int64) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO roles (name, default_config_id) VALUES (?, ?)`, name, defaultConfigID)
	if err != nil {
		return 0, fmt.Errorf("db: create role %q: %w", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("db: create role id: %w", err)
	}
	return id, nil
}

// GetRole returns a role by id, or ErrNotFound.
func (s *Store) GetRole(id int64) (*Role, error) {
	var r Role
	err := s.db.QueryRow(
		`SELECT id, name, default_config_id, created_at, updated_at FROM roles WHERE id = ?`, id,
	).Scan(&r.ID, &r.Name, &r.DefaultConfigID, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get role %d: %w", id, err)
	}
	return &r, nil
}

// ListRoles returns every role with its bound-host count, ordered by name.
func (s *Store) ListRoles() ([]RoleListRow, error) {
	rows, err := s.db.Query(
		`SELECT r.id, r.name, r.default_config_id,
		        (SELECT COUNT(*) FROM host_roles hr WHERE hr.role_id = r.id)
		   FROM roles r ORDER BY r.name`)
	if err != nil {
		return nil, fmt.Errorf("db: list roles: %w", err)
	}
	defer rows.Close()
	var out []RoleListRow
	for rows.Next() {
		var r RoleListRow
		if err := rows.Scan(&r.ID, &r.Name, &r.DefaultConfigID, &r.HostCount); err != nil {
			return nil, fmt.Errorf("db: scan role: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateRole updates name and/or default_config_id (nil pointers = leave as-is).
// A nil defaultConfigID pointer leaves the column untouched; to CLEAR it, pass a
// pointer to a sentinel handled by the caller — P4's API does not expose clear,
// so a nil pointer is "unchanged" only.
func (s *Store) UpdateRole(id int64, name *string, defaultConfigID *int64) error {
	if name != nil {
		if _, err := s.db.Exec(
			`UPDATE roles SET name = ?, updated_at = datetime('now') WHERE id = ?`, *name, id); err != nil {
			return fmt.Errorf("db: update role name %d: %w", id, err)
		}
	}
	if defaultConfigID != nil {
		if _, err := s.db.Exec(
			`UPDATE roles SET default_config_id = ?, updated_at = datetime('now') WHERE id = ?`,
			*defaultConfigID, id); err != nil {
			return fmt.Errorf("db: update role default %d: %w", id, err)
		}
	}
	return nil
}

// SetHostConfig sets (or clears, when configID is nil) hosts.config_id for mac.
// The last_seen bump is intentional pattern-consistency with SetAssignment /
// Approve / Deny (F7): every host-touch accessor refreshes last_seen.
func (s *Store) SetHostConfig(mac string, configID *int64) error {
	if _, err := s.db.Exec(
		// last_seen bump mirrors SetAssignment (intentional host-touch consistency, F7).
		`UPDATE hosts SET config_id = ?, last_seen = datetime('now') WHERE mac = ?`, configID, mac); err != nil {
		return fmt.Errorf("db: set host config %s: %w", mac, err)
	}
	return nil
}

// SetHostRoles replaces mac's entire host_roles set with roleIDs (transactional).
// An empty/nil slice unbinds all roles.
func (s *Store) SetHostRoles(mac string, roleIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("db: set host roles begin %s: %w", mac, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM host_roles WHERE host_mac = ?`, mac); err != nil {
		return fmt.Errorf("db: clear host roles %s: %w", mac, err)
	}
	for _, rid := range roleIDs {
		if _, err := tx.Exec(
			`INSERT INTO host_roles (host_mac, role_id) VALUES (?, ?)`, mac, rid); err != nil {
			return fmt.Errorf("db: bind role %d to %s: %w", rid, mac, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: set host roles commit %s: %w", mac, err)
	}
	return nil
}

// ListHostRoles returns mac's roles ordered by name (feeds the .Roles template
// var and the by-name role-default precedence rung).
func (s *Store) ListHostRoles(mac string) ([]Role, error) {
	rows, err := s.db.Query(
		`SELECT r.id, r.name, r.default_config_id, r.created_at, r.updated_at
		   FROM roles r
		   JOIN host_roles hr ON hr.role_id = r.id
		  WHERE hr.host_mac = ? ORDER BY r.name`, mac)
	if err != nil {
		return nil, fmt.Errorf("db: list host roles %s: %w", mac, err)
	}
	defer rows.Close()
	var out []Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.Name, &r.DefaultConfigID, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("db: scan host role: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
