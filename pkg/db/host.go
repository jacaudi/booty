package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// Host is the projection of the hosts table that pkg/hardware and the P1c
// target/boot-dispatch API read and write. Legacy columns are preserved exactly;
// the approval/assignment columns are added here for P1c.
type Host struct {
	MAC          string
	Hostname     string
	IP           string
	Booted       string
	IgnitionFile string
	OS           string
	DoInstall    bool
	Schematic    string

	Approved       bool
	BootMode       string // 'assigned' | 'menu'
	AssignedOS     string
	AssignedArch   string
	AssignedParams string
	UUID           string
	Serial         string
}

const hostCols = `mac, hostname, ip, booted, ignition_file, os, do_install, schematic, ` +
	`approved, boot_mode, assigned_os, assigned_arch, assigned_params, uuid, serial`

func scanHost(scan func(...any) error) (Host, error) {
	var h Host
	err := scan(&h.MAC, &h.Hostname, &h.IP, &h.Booted, &h.IgnitionFile, &h.OS, &h.DoInstall, &h.Schematic,
		&h.Approved, &h.BootMode, &h.AssignedOS, &h.AssignedArch, &h.AssignedParams, &h.UUID, &h.Serial)
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

// ApproveHost marks mac approved. Idempotent; absent mac is a no-op.
func (s *Store) ApproveHost(mac string) error {
	if _, err := s.db.Exec(`UPDATE hosts SET approved = 1, last_seen = datetime('now') WHERE mac = ?`, mac); err != nil {
		return fmt.Errorf("db: approve host %s: %w", mac, err)
	}
	return nil
}

// RevokeHost clears the approved flag for mac. Idempotent.
func (s *Store) RevokeHost(mac string) error {
	if _, err := s.db.Exec(`UPDATE hosts SET approved = 0, last_seen = datetime('now') WHERE mac = ?`, mac); err != nil {
		return fmt.Errorf("db: revoke host %s: %w", mac, err)
	}
	return nil
}

// SetAssignment sets boot_mode='assigned' and the assigned target for mac.
// params MUST be the canonical encoding (cache.EncodeParams).
func (s *Store) SetAssignment(mac, os, arch, params string) error {
	if _, err := s.db.Exec(
		`UPDATE hosts SET boot_mode = 'assigned', assigned_os = ?, assigned_arch = ?, assigned_params = ?,
		   last_seen = datetime('now') WHERE mac = ?`,
		os, arch, params, mac); err != nil {
		return fmt.Errorf("db: assign host %s: %w", mac, err)
	}
	return nil
}

// PreserveExistingHostBoot is a one-time upgrade backfill: it marks every
// already-registered host (os != '') approved + boot_mode='assigned' +
// assigned_os=os, so hosts that booted under the pre-P1c (host.OS-driven) path
// keep booting identically instead of dropping to the holding pattern. Gated by
// the meta flag "host_boot_preserved" so it runs exactly once at the upgrade
// boundary; hosts registered AFTER it stay unapproved (holding until approved).
// Returns the number of rows updated. Idempotent across restarts.
func (s *Store) PreserveExistingHostBoot() (int64, error) {
	if v, err := s.GetMeta("host_boot_preserved"); err == nil && v == "1" {
		return 0, nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("db: read preserve flag: %w", err)
	}
	res, err := s.db.Exec(
		`UPDATE hosts SET approved = 1, boot_mode = 'assigned', assigned_os = os
		   WHERE os != '' AND approved = 0`)
	if err != nil {
		return 0, fmt.Errorf("db: preserve host boot: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := s.SetMeta("host_boot_preserved", "1"); err != nil {
		return n, fmt.Errorf("db: set preserve flag: %w", err)
	}
	return n, nil
}
