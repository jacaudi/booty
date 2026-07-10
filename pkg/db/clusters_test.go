package db

import (
	"errors"
	"testing"
)

func testCluster(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id, err := s.CreateCluster(name, "https://10.0.0.10:6443", "v1.13.5", "v1.34.0", []byte("enc-bundle"))
	if err != nil {
		t.Fatalf("CreateCluster(%s): %v", name, err)
	}
	return id
}

func TestClusterCRUD(t *testing.T) {
	s := newTestStore(t)
	id := testCluster(t, s, "prod")

	c, err := s.GetCluster(id)
	if err != nil {
		t.Fatalf("GetCluster: %v", err)
	}
	if c.Name != "prod" || c.Endpoint != "https://10.0.0.10:6443" ||
		c.TalosVersion != "v1.13.5" || c.K8sVersion != "v1.34.0" ||
		string(c.SecretsEnc) != "enc-bundle" || c.SpecConfigID != nil {
		t.Fatalf("cluster round-trip = %+v", c)
	}

	// Duplicate name violates UNIQUE.
	if _, err := s.CreateCluster("prod", "https://e:6443", "v1.13.5", "v1.34.0", []byte("x")); err == nil {
		t.Fatal("duplicate cluster name accepted")
	}

	// Missing cluster is ErrNotFound.
	if _, err := s.GetCluster(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCluster(miss) = %v, want ErrNotFound", err)
	}

	// Update repoints fields + spec binding, bumps updated_at.
	cfgID, err := s.CreateConfig("spec", "taloscluster")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateCluster(id, "https://10.0.0.99:6443", "v1.13.9", "v1.35.0", &cfgID); err != nil {
		t.Fatalf("UpdateCluster: %v", err)
	}
	c, _ = s.GetCluster(id)
	if c.Endpoint != "https://10.0.0.99:6443" || c.TalosVersion != "v1.13.9" ||
		c.K8sVersion != "v1.35.0" || c.SpecConfigID == nil || *c.SpecConfigID != cfgID {
		t.Fatalf("cluster after update = %+v", c)
	}

	list, err := s.ListClusters()
	if err != nil || len(list) != 1 || list[0].Name != "prod" {
		t.Fatalf("ListClusters = %+v, err %v", list, err)
	}
}

func TestListClusterMembers(t *testing.T) {
	s := newTestStore(t)
	id := testCluster(t, s, "members")
	other := testCluster(t, s, "other")

	for _, mac := range []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "aa:bb:cc:dd:ee:03"} {
		if err := s.UpsertHost(Host{MAC: mac, OS: "talos"}); err != nil {
			t.Fatal(err)
		}
	}
	// Two members of `id`, one of `other`, written raw (setters land in Task 4).
	for _, stmt := range []struct {
		mac string
		cid int64
		mt  string
	}{
		{"aa:bb:cc:dd:ee:01", id, "controlplane"},
		{"aa:bb:cc:dd:ee:02", id, "worker"},
		{"aa:bb:cc:dd:ee:03", other, "controlplane"},
	} {
		if _, err := s.db.Exec(`UPDATE hosts SET cluster_id = ?, machine_type = ? WHERE mac = ?`,
			stmt.cid, stmt.mt, stmt.mac); err != nil {
			t.Fatal(err)
		}
	}

	members, err := s.ListClusterMembers(id)
	if err != nil {
		t.Fatalf("ListClusterMembers: %v", err)
	}
	if len(members) != 2 || members[0].MAC != "aa:bb:cc:dd:ee:01" || members[1].MachineType != "worker" {
		t.Fatalf("members = %+v", members)
	}
}
