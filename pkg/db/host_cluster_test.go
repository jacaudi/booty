package db

import "testing"

func TestSetHostClusterMembershipColumns(t *testing.T) {
	s := newTestStore(t)
	cid := testCluster(t, s, "setters")
	const mac = "aa:bb:cc:dd:ee:40"
	if err := s.UpsertHost(Host{MAC: mac, OS: "talos"}); err != nil {
		t.Fatal(err)
	}
	ncID, _, err := s.AddClusterNodeConfig(mac, cid, []byte("enc"), "sha", "generated", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetHostCluster(mac, &cid); err != nil {
		t.Fatalf("SetHostCluster: %v", err)
	}
	if err := s.SetHostMachineType(mac, "worker"); err != nil {
		t.Fatalf("SetHostMachineType: %v", err)
	}
	if err := s.SetHostNodeConfig(mac, &ncID); err != nil {
		t.Fatalf("SetHostNodeConfig: %v", err)
	}
	h, err := s.GetHost(mac)
	if err != nil {
		t.Fatal(err)
	}
	if h.ClusterID == nil || *h.ClusterID != cid || h.MachineType != "worker" ||
		h.NodeConfigID == nil || *h.NodeConfigID != ncID {
		t.Fatalf("membership after set = %+v", h)
	}

	// Clearing: nil / "" revert every column to NULL (remove-member, §6.4).
	if err := s.SetHostNodeConfig(mac, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostMachineType(mac, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostCluster(mac, nil); err != nil {
		t.Fatal(err)
	}
	h, _ = s.GetHost(mac)
	if h.ClusterID != nil || h.MachineType != "" || h.NodeConfigID != nil {
		t.Fatalf("membership not cleared: %+v", h)
	}
}
