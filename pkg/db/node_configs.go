package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// ClusterNodeConfig is one frozen, age-encrypted machineconfig revision for a
// cluster member (P6 design §3/§5): ConfigEnc is the age ciphertext; SHA256
// hashes the PLAINTEXT bytes (integrity / change detection); Source records
// how the bytes were born ('generated' | 'imported'). Revisions are immutable
// and append-only; a host serves the revision hosts.node_config_id points at.
// This store deliberately mirrors the SHAPE of P4's config_revisions without
// sharing its code — encrypted per-host frozen bytes vs plaintext per-config
// source are different knowledge (D10/M5).
type ClusterNodeConfig struct {
	ID        int64
	MAC       string
	ClusterID int64
	Revision  int
	ConfigEnc []byte
	SHA256    string
	Source    string
	HostPatch string // the per-host strategic-merge patch that produced ConfigEnc; "" = none/imported
	CreatedAt string
}

// AddClusterNodeConfig appends an immutable frozen revision (revision = max+1
// per mac) and returns its row id and revision number. It does NOT advance
// hosts.node_config_id; the caller does, mirroring AddConfigRevision /
// SetActiveRevision (P4). hostPatch is the per-host patch that produced these
// bytes — a durable generation input co-located with its frozen output;
// "" stores NULL (imported rows and patch-less binds).
func (s *Store) AddClusterNodeConfig(mac string, clusterID int64, configEnc []byte, sha256, source, hostPatch string) (int64, int, error) {
	var next int
	if err := s.db.QueryRow(
		`SELECT COALESCE(MAX(revision), 0) + 1 FROM cluster_node_configs WHERE mac = ?`,
		mac).Scan(&next); err != nil {
		return 0, 0, fmt.Errorf("db: next node-config revision for %s: %w", mac, err)
	}
	res, err := s.db.Exec(
		`INSERT INTO cluster_node_configs (mac, cluster_id, revision, config_enc, sha256, source, host_patch)
		 VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''))`, mac, clusterID, next, configEnc, sha256, source, hostPatch)
	if err != nil {
		return 0, 0, fmt.Errorf("db: add node config %s: %w", mac, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, 0, fmt.Errorf("db: add node config id: %w", err)
	}
	return id, next, nil
}

// GetClusterNodeConfig returns a frozen revision by row id, or ErrNotFound. A
// NULL host_patch scans back as "".
func (s *Store) GetClusterNodeConfig(id int64) (*ClusterNodeConfig, error) {
	var n ClusterNodeConfig
	var patch sql.NullString
	err := s.db.QueryRow(
		`SELECT id, mac, cluster_id, revision, config_enc, sha256, source, host_patch, created_at
		   FROM cluster_node_configs WHERE id = ?`, id,
	).Scan(&n.ID, &n.MAC, &n.ClusterID, &n.Revision, &n.ConfigEnc, &n.SHA256, &n.Source, &patch, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get node config %d: %w", id, err)
	}
	n.HostPatch = patch.String
	return &n, nil
}

// DeleteClusterNodeConfigs prunes every frozen revision the mac holds for the
// cluster (remove-member, design §6.4). Idempotent.
func (s *Store) DeleteClusterNodeConfigs(mac string, clusterID int64) error {
	if _, err := s.db.Exec(
		`DELETE FROM cluster_node_configs WHERE mac = ? AND cluster_id = ?`, mac, clusterID); err != nil {
		return fmt.Errorf("db: delete node configs %s/%d: %w", mac, clusterID, err)
	}
	return nil
}

// SchematicVersion is one (schematic, version) retention pin. Schematic may be
// "" (a member without an explicit schematic, or a memberless cluster) — the
// consumer resolves "" to the --talosSchematic default, the same resolution
// the boot path applies.
type SchematicVersion struct {
	Schematic string
	Version   string
}

// ClusterReferencedVersions returns the DISTINCT (schematic, talos_version)
// pairs referenced by live clusters — the P6 never-evict retention input
// (design §8/D4/M3). LEFT JOIN so a memberless cluster still pins its version
// under the default schematic.
func (s *Store) ClusterReferencedVersions() ([]SchematicVersion, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT COALESCE(h.schematic, ''), c.talos_version
		   FROM clusters c LEFT JOIN hosts h ON h.cluster_id = c.id`)
	if err != nil {
		return nil, fmt.Errorf("db: cluster referenced versions: %w", err)
	}
	defer rows.Close()
	var out []SchematicVersion
	for rows.Next() {
		var sv SchematicVersion
		if err := rows.Scan(&sv.Schematic, &sv.Version); err != nil {
			return nil, fmt.Errorf("db: scan referenced version: %w", err)
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}
