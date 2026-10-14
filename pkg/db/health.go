package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeefy/booty/pkg/nodereport"
)

// UpsertHostHealth stores the latest health report for a host and marks the
// host as seen, in one transaction. It fails when the host does not exist
// (foreign key).
func (s *Store) UpsertHostHealth(ctx context.Context, mac, receivedAt string, report []byte) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin host health upsert: %w", err)
	}
	defer func() {
		// After Commit the rollback reports sql.ErrTxDone, which is not an error here.
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			err = errors.Join(err, rbErr)
		}
	}()

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
func (s *Store) GetHostHealth(ctx context.Context, mac string) (string, []byte, error) {
	var receivedAt, report string
	err := s.db.QueryRowContext(ctx,
		`SELECT received_at, report FROM host_health WHERE mac = ?`, mac,
	).Scan(&receivedAt, &report)
	if err != nil {
		return "", nil, err
	}
	return receivedAt, []byte(report), nil
}

// ListHostHealthSummaries returns the list-view summary of every stored
// report keyed by MAC, extracted by SQLite so a refresh does not read and
// decode every full report (up to 64 KiB each). A nil value marks a report
// that is stored but not a JSON object, which [nodereport.Health] could not
// decode either; a host that never reported is absent. Every field is
// guarded so one corrupt row cannot fail the whole listing.
func (s *Store) ListHostHealthSummaries(ctx context.Context) (map[string]*nodereport.Summary, error) {
	// j is the report when it is a JSON object, else NULL; the nested CASE
	// keeps json_type off invalid JSON, which would raise "malformed JSON".
	rows, err := s.db.QueryContext(ctx, `
		SELECT mac, j IS NOT NULL,
		       COALESCE(json_extract(j, '$.running'), ''),
		       COALESCE(json_extract(j, '$.kernel'), ''),
		       COALESCE(json_extract(j, '$.firmware'), ''),
		       COALESCE(json_extract(j, '$.receivedAt'), ''),
		       COALESCE(json_array_length(j, '$.failedUnits'), 0),
		       COALESCE(json_extract(j, '$.dmi.vendor'), ''),
		       COALESCE(json_extract(j, '$.dmi.product'), '')
		FROM (SELECT mac, CASE WHEN json_valid(report)
		                       THEN CASE WHEN json_type(report) = 'object' THEN report END
		                  END AS j
		      FROM host_health)`)
	if err != nil {
		return nil, fmt.Errorf("list host health summaries: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only; rows.Err below reports iteration failures
	out := make(map[string]*nodereport.Summary)
	for rows.Next() {
		var (
			mac string
			ok  bool
			sum nodereport.Summary
		)
		if err = rows.Scan(&mac, &ok, &sum.Running, &sum.Kernel, &sum.Firmware, &sum.ReceivedAt,
			&sum.FailedUnitCount, &sum.Vendor, &sum.Product); err != nil {
			return nil, fmt.Errorf("scan host health summary: %w", err)
		}
		if ok {
			out[mac] = &sum
		} else {
			out[mac] = nil
		}
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list host health summaries: %w", err)
	}
	return out, nil
}

// HostReportTokenGen returns the host's report-token generation, or
// [sql.ErrNoRows] when the host is absent.
func (s *Store) HostReportTokenGen(ctx context.Context, mac string) (int64, error) {
	var gen int64
	err := s.db.QueryRowContext(ctx, `SELECT report_token_gen FROM hosts WHERE mac = ?`, mac).
		Scan(&gen)
	if err != nil {
		return 0, err
	}
	return gen, nil
}

// BumpHostReportTokenGen increments the generation (invalidating the host's
// old token) and returns the new value, or [sql.ErrNoRows] for an absent host.
func (s *Store) BumpHostReportTokenGen(ctx context.Context, mac string) (int64, error) {
	var gen int64
	err := s.db.QueryRowContext(ctx,
		`UPDATE hosts SET report_token_gen = report_token_gen + 1 WHERE mac = ? RETURNING report_token_gen`, mac,
	).Scan(&gen)
	if err != nil {
		return 0, err
	}
	return gen, nil
}
