package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// Cluster is one authored Talos cluster (P6 design §3): pinned versions +
// endpoint are STRUCTURED fields (D7 — reproducibility-critical, never buried
// in YAML); SpecConfigID points at the taloscluster-kind config carrying
// cluster-wide + role patches; SecretsEnc is the age-encrypted secrets bundle.
type Cluster struct {
	ID           int64
	Name         string
	Endpoint     string
	TalosVersion string
	K8sVersion   string
	SpecConfigID *int64
	SecretsEnc   []byte
	CreatedAt    string
	UpdatedAt    string
}

// CreateCluster inserts a cluster and returns its id. A duplicate name
// violates UNIQUE and returns an error.
func (s *Store) CreateCluster(name, endpoint, talosVersion, k8sVersion string, secretsEnc []byte) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO clusters (name, endpoint, talos_version, k8s_version, secrets_enc)
		 VALUES (?, ?, ?, ?, ?)`, name, endpoint, talosVersion, k8sVersion, secretsEnc)
	if err != nil {
		return 0, fmt.Errorf("db: create cluster %q: %w", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("db: create cluster id: %w", err)
	}
	return id, nil
}

// GetCluster returns the cluster, or ErrNotFound.
func (s *Store) GetCluster(id int64) (*Cluster, error) {
	var c Cluster
	var spec sql.NullInt64
	err := s.db.QueryRow(
		`SELECT id, name, endpoint, talos_version, k8s_version, spec_config_id, secrets_enc, created_at, updated_at
		   FROM clusters WHERE id = ?`, id,
	).Scan(&c.ID, &c.Name, &c.Endpoint, &c.TalosVersion, &c.K8sVersion, &spec, &c.SecretsEnc, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get cluster %d: %w", id, err)
	}
	if spec.Valid {
		c.SpecConfigID = &spec.Int64
	}
	return &c, nil
}

// ListClusters returns every cluster ordered by name.
func (s *Store) ListClusters() ([]Cluster, error) {
	rows, err := s.db.Query(
		`SELECT id, name, endpoint, talos_version, k8s_version, spec_config_id, secrets_enc, created_at, updated_at
		   FROM clusters ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("db: list clusters: %w", err)
	}
	defer rows.Close()
	var out []Cluster
	for rows.Next() {
		var c Cluster
		var spec sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Name, &c.Endpoint, &c.TalosVersion, &c.K8sVersion, &spec, &c.SecretsEnc, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("db: scan cluster: %w", err)
		}
		if spec.Valid {
			c.SpecConfigID = &spec.Int64
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateCluster rewrites the mutable pinned inputs (endpoint, versions, spec
// binding) and bumps updated_at. Secrets are immutable through this path
// (Rotate is deferred, D5). Regeneration of member configs flows through
// re-bind, not through this write (plan divergence D-C).
func (s *Store) UpdateCluster(id int64, endpoint, talosVersion, k8sVersion string, specConfigID *int64) error {
	if _, err := s.db.Exec(
		`UPDATE clusters SET endpoint = ?, talos_version = ?, k8s_version = ?, spec_config_id = ?,
		   updated_at = datetime('now') WHERE id = ?`,
		endpoint, talosVersion, k8sVersion, specConfigID, id); err != nil {
		return fmt.Errorf("db: update cluster %d: %w", id, err)
	}
	return nil
}

// DeleteCluster removes a cluster row by id. It is the store-level primitive the
// multi-host import rollback uses to undo a failed adoption — the HTTP
// delete-cluster handler is 403 until auth (P10), so rollback cannot go through
// it. Callers MUST clear child rows first (member hosts' cluster_id +
// cluster_node_configs): with foreign_keys=ON a row still referenced by
// hosts.cluster_id or cluster_node_configs.cluster_id will not delete. Deleting
// an absent id is a no-op (SQLite DELETE affects zero rows without error), so
// rollback can call it unconditionally.
func (s *Store) DeleteCluster(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM clusters WHERE id = ?`, id); err != nil {
		return fmt.Errorf("db: delete cluster %d: %w", id, err)
	}
	return nil
}

// ListClusterMembers returns the cluster's member hosts ordered by mac —
// membership lives on hosts columns (design §3), so this is a hosts
// projection, reusing the standard host scan.
func (s *Store) ListClusterMembers(clusterID int64) ([]Host, error) {
	rows, err := s.db.Query(`SELECT `+hostCols+` FROM hosts WHERE cluster_id = ? ORDER BY mac`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("db: list cluster members %d: %w", clusterID, err)
	}
	defer rows.Close()
	var out []Host
	for rows.Next() {
		h, err := scanHost(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("db: scan member: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
