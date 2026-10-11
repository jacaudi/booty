package db

import (
	"context"
	"fmt"
)

// UpsertHostHealth stores the latest health report for a host and marks the
// host as seen, in one transaction. It fails when the host does not exist
// (foreign key).
func (s *Store) UpsertHostHealth(mac, receivedAt string, report []byte) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin host health upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx,
		`INSERT INTO host_health (mac, received_at, report) VALUES (?, ?, ?)
		 ON CONFLICT(mac) DO UPDATE SET received_at = excluded.received_at, report = excluded.report`,
		mac, receivedAt, string(report)); err != nil {
		return fmt.Errorf("upsert host health %s: %w", mac, err)
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE hosts SET last_seen = datetime('now') WHERE mac = ?`, mac); err != nil {
		return fmt.Errorf("touch host %s: %w", mac, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit host health upsert: %w", err)
	}
	return nil
}

// GetHostHealth returns the stored report for mac, or [sql.ErrNoRows].
func (s *Store) GetHostHealth(mac string) (string, []byte, error) {
	var receivedAt, report string
	err := s.db.QueryRowContext(context.Background(),
		`SELECT received_at, report FROM host_health WHERE mac = ?`, mac,
	).Scan(&receivedAt, &report)
	if err != nil {
		return "", nil, err
	}
	return receivedAt, []byte(report), nil
}

// ListHostHealth returns every stored report keyed by MAC.
func (s *Store) ListHostHealth() (map[string][]byte, error) {
	rows, err := s.db.QueryContext(context.Background(), `SELECT mac, report FROM host_health`)
	if err != nil {
		return nil, fmt.Errorf("list host health: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]byte)
	for rows.Next() {
		var mac, report string
		if err = rows.Scan(&mac, &report); err != nil {
			return nil, fmt.Errorf("scan host health: %w", err)
		}
		out[mac] = []byte(report)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list host health: %w", err)
	}
	return out, nil
}

// HostReportTokenGen returns the host's report-token generation, or
// [sql.ErrNoRows] when the host is absent.
func (s *Store) HostReportTokenGen(mac string) (int64, error) {
	var gen int64
	err := s.db.QueryRowContext(context.Background(), `SELECT report_token_gen FROM hosts WHERE mac = ?`, mac).
		Scan(&gen)
	if err != nil {
		return 0, err
	}
	return gen, nil
}

// BumpHostReportTokenGen increments the generation (invalidating the host's
// old token) and returns the new value, or [sql.ErrNoRows] for an absent host.
func (s *Store) BumpHostReportTokenGen(mac string) (int64, error) {
	var gen int64
	err := s.db.QueryRowContext(context.Background(),
		`UPDATE hosts SET report_token_gen = report_token_gen + 1 WHERE mac = ? RETURNING report_token_gen`, mac,
	).Scan(&gen)
	if err != nil {
		return 0, err
	}
	return gen, nil
}
