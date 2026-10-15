package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// DiscoveredHost is a machine that reached the boot path but is not yet a
// registered host. FirstSeen and LastSeen are SQLite datetime values in UTC.
type DiscoveredHost struct {
	MAC       string
	Arch      string
	IP        string
	FirstSeen string
	LastSeen  string
}

// TouchDiscoveredHost refreshes the row for mac and reports whether it exists.
// The write is debounced: a row seen within the last 20 seconds is left as is,
// and the call still reports true. An empty arch keeps the stored arch.
func (s *Store) TouchDiscoveredHost(ctx context.Context, mac, ip, arch string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM discovered_hosts WHERE mac = ?`, mac).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("db: touch discovered host %s: %w", mac, err)
	}
	if _, err = s.db.ExecContext(ctx,
		`UPDATE discovered_hosts
		   SET last_seen = datetime('now'), ip = ?, arch = CASE WHEN ? = '' THEN arch ELSE ? END
		 WHERE mac = ? AND last_seen <= datetime('now','-20 seconds')`,
		ip, arch, arch, mac); err != nil {
		return false, fmt.Errorf("db: touch discovered host %s: %w", mac, err)
	}
	return true, nil
}

// InsertDiscoveredHost records mac and reports whether a row was added. It adds
// nothing, without an error, when the table already holds maxRows rows, when mac
// is already discovered, or when mac is a registered host.
func (s *Store) InsertDiscoveredHost(ctx context.Context, mac, ip, arch string, maxRows int) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO discovered_hosts (mac, ip, arch)
		 SELECT ?, ?, ?
		 WHERE (SELECT count(*) FROM discovered_hosts) < ?
		   AND NOT EXISTS (SELECT 1 FROM hosts WHERE mac = ?)
		 ON CONFLICT(mac) DO NOTHING`,
		mac, ip, arch, maxRows, mac)
	if err != nil {
		return false, fmt.Errorf("db: insert discovered host %s: %w", mac, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("db: insert discovered host %s: %w", mac, err)
	}
	return n == 1, nil
}

// ListDiscoveredHosts returns the discovered hosts that are not registered,
// most recently seen first. It returns nil when there are none.
func (s *Store) ListDiscoveredHosts(ctx context.Context) ([]DiscoveredHost, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.mac, d.arch, d.ip, d.first_seen, d.last_seen
		 FROM discovered_hosts d
		 WHERE NOT EXISTS (SELECT 1 FROM hosts h WHERE h.mac = d.mac)
		 ORDER BY d.last_seen DESC, d.mac ASC`)
	if err != nil {
		return nil, fmt.Errorf("db: list discovered hosts: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only; rows.Err below reports iteration failures

	var out []DiscoveredHost
	for rows.Next() {
		var d DiscoveredHost
		if err = rows.Scan(&d.MAC, &d.Arch, &d.IP, &d.FirstSeen, &d.LastSeen); err != nil {
			return nil, fmt.Errorf("db: scan discovered host: %w", err)
		}
		out = append(out, d)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list discovered hosts: %w", err)
	}
	return out, nil
}

// CountDiscoveredHosts returns the number of stored discovered-host rows.
func (s *Store) CountDiscoveredHosts(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM discovered_hosts`).Scan(&n); err != nil {
		return 0, fmt.Errorf("db: count discovered hosts: %w", err)
	}
	return n, nil
}

// DeleteDiscoveredHost removes the row for mac and reports whether one existed.
func (s *Store) DeleteDiscoveredHost(ctx context.Context, mac string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM discovered_hosts WHERE mac = ?`, mac)
	if err != nil {
		return false, fmt.Errorf("db: delete discovered host %s: %w", mac, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("db: delete discovered host %s: %w", mac, err)
	}
	return n == 1, nil
}

// DeleteAllDiscoveredHosts removes every row and returns how many it removed.
func (s *Store) DeleteAllDiscoveredHosts(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM discovered_hosts`)
	if err != nil {
		return 0, fmt.Errorf("db: delete discovered hosts: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("db: delete discovered hosts: %w", err)
	}
	return n, nil
}
