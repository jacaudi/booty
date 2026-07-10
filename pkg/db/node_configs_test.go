package db

import (
	"errors"
	"testing"
)

func TestClusterNodeConfigRevisions(t *testing.T) {
	s := newTestStore(t)
	cid := testCluster(t, s, "ncfg")
	const mac = "aa:bb:cc:dd:ee:10"

	id1, rev1, err := s.AddClusterNodeConfig(mac, cid, []byte("enc-v1"), "sha1", "generated", "machine:\n  certSANs: [1.2.3.4]\n")
	if err != nil {
		t.Fatalf("AddClusterNodeConfig: %v", err)
	}
	if rev1 != 1 {
		t.Fatalf("first revision = %d, want 1", rev1)
	}
	id2, rev2, err := s.AddClusterNodeConfig(mac, cid, []byte("enc-v2"), "sha2", "imported", "")
	if err != nil || rev2 != 2 {
		t.Fatalf("second revision = %d, err %v, want 2", rev2, err)
	}
	if id1 == id2 {
		t.Fatal("revision ids must differ")
	}

	// The first revision persisted its per-host patch; the second (imported,
	// "") stored NULL and scans back as "".
	got1, err := s.GetClusterNodeConfig(id1)
	if err != nil {
		t.Fatal(err)
	}
	if got1.HostPatch != "machine:\n  certSANs: [1.2.3.4]\n" {
		t.Fatalf("host_patch not persisted: %q", got1.HostPatch)
	}
	got, err := s.GetClusterNodeConfig(id2)
	if err != nil {
		t.Fatalf("GetClusterNodeConfig: %v", err)
	}
	if got.MAC != mac || got.ClusterID != cid || got.Revision != 2 ||
		string(got.ConfigEnc) != "enc-v2" || got.SHA256 != "sha2" || got.Source != "imported" || got.HostPatch != "" {
		t.Fatalf("node config round-trip = %+v", got)
	}

	if _, err := s.GetClusterNodeConfig(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetClusterNodeConfig(miss) = %v, want ErrNotFound", err)
	}

	// Delete prunes all of the mac's revisions for that cluster.
	if err := s.DeleteClusterNodeConfigs(mac, cid); err != nil {
		t.Fatalf("DeleteClusterNodeConfigs: %v", err)
	}
	if _, err := s.GetClusterNodeConfig(id1); !errors.Is(err, ErrNotFound) {
		t.Fatal("revisions not pruned after delete")
	}
}

func TestClusterReferencedVersions(t *testing.T) {
	s := newTestStore(t)
	c1 := testCluster(t, s, "pin1") // v1.13.5 (testCluster's fixed version)
	c2 := testCluster(t, s, "pin2") // memberless — must still pin its version

	if err := s.UpsertHost(Host{MAC: "aa:bb:cc:dd:ee:20", OS: "talos", Schematic: "schemA"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertHost(Host{MAC: "aa:bb:cc:dd:ee:21", OS: "talos"}); err != nil { // empty schematic
		t.Fatal(err)
	}
	for _, mac := range []string{"aa:bb:cc:dd:ee:20", "aa:bb:cc:dd:ee:21"} {
		if _, err := s.db.Exec(`UPDATE hosts SET cluster_id = ? WHERE mac = ?`, c1, mac); err != nil {
			t.Fatal(err)
		}
	}
	_ = c2

	refs, err := s.ClusterReferencedVersions()
	if err != nil {
		t.Fatalf("ClusterReferencedVersions: %v", err)
	}
	want := map[SchematicVersion]bool{
		{Schematic: "schemA", Version: "v1.13.5"}: true, // member with explicit schematic
		{Schematic: "", Version: "v1.13.5"}:       true, // empty-schematic member AND the memberless cluster
	}
	if len(refs) != len(want) {
		t.Fatalf("refs = %+v, want keys %+v", refs, want)
	}
	for _, r := range refs {
		if !want[r] {
			t.Fatalf("unexpected ref %+v", r)
		}
	}
}
