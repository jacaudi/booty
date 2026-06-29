package db

import "fmt"

// Host is the legacy-compatible projection of the hosts table — exactly the
// columns pkg/hardware reads and writes today. The remaining hosts columns
// (approved, boot_mode, assigned_*, uuid, serial, first_seen, last_seen) are
// owned by later slices; P1a never writes them, so they keep their defaults.
type Host struct {
	MAC          string
	Hostname     string
	IP           string
	Booted       string
	IgnitionFile string
	OS           string
	DoInstall    bool
	Schematic    string
}

const hostCols = `mac, hostname, ip, booted, ignition_file, os, do_install, schematic`

func scanHost(scan func(...any) error) (Host, error) {
	var h Host
	err := scan(&h.MAC, &h.Hostname, &h.IP, &h.Booted, &h.IgnitionFile, &h.OS, &h.DoInstall, &h.Schematic)
	return h, err
}

// GetHost returns the host for the (already-canonical) mac, or sql.ErrNoRows.
func (s *Store) GetHost(mac string) (*Host, error) {
	row := s.db.QueryRow(`SELECT `+hostCols+` FROM hosts WHERE mac = ?`, mac)
	h, err := scanHost(row.Scan)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// UpsertHost inserts or updates ONLY the legacy columns for h.MAC and refreshes
// last_seen, leaving the later-slice columns (approved, boot_mode, …) untouched.
func (s *Store) UpsertHost(h Host) error {
	_, err := s.db.Exec(
		`INSERT INTO hosts (mac, hostname, ip, booted, ignition_file, os, do_install, schematic)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(mac) DO UPDATE SET
		   hostname      = excluded.hostname,
		   ip            = excluded.ip,
		   booted        = excluded.booted,
		   ignition_file = excluded.ignition_file,
		   os            = excluded.os,
		   do_install    = excluded.do_install,
		   schematic     = excluded.schematic,
		   last_seen     = datetime('now')`,
		h.MAC, h.Hostname, h.IP, h.Booted, h.IgnitionFile, h.OS, h.DoInstall, h.Schematic,
	)
	if err != nil {
		return fmt.Errorf("db: upsert host %s: %w", h.MAC, err)
	}
	return nil
}

// DeleteHost removes the host with mac. Removing an absent host is a no-op.
func (s *Store) DeleteHost(mac string) error {
	if _, err := s.db.Exec(`DELETE FROM hosts WHERE mac = ?`, mac); err != nil {
		return fmt.Errorf("db: delete host %s: %w", mac, err)
	}
	return nil
}

// ListHosts returns all hosts ordered by mac.
func (s *Store) ListHosts() ([]Host, error) {
	rows, err := s.db.Query(`SELECT ` + hostCols + ` FROM hosts ORDER BY mac`)
	if err != nil {
		return nil, fmt.Errorf("db: list hosts: %w", err)
	}
	defer rows.Close()

	var out []Host
	for rows.Next() {
		h, err := scanHost(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("db: scan host: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
