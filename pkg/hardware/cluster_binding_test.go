package hardware

import (
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/db"
)

func clusterTestStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.Open(filepath.Join(t.TempDir(), "booty.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	SetStore(s)
	t.Cleanup(func() { SetStore(nil); s.Close() })
	return s
}

func TestClusterMembershipWrappers(t *testing.T) {
	s := clusterTestStore(t)
	const mac = "AA-BB-CC-DD-EE-41" // non-canonical on purpose: wrappers normalize
	canonical := "aa:bb:cc:dd:ee:41"
	if err := WriteMacAddress(mac, Host{OS: "talos"}); err != nil {
		t.Fatal(err)
	}
	cid, err := s.CreateCluster("wrap", "https://e:6443", "v1.13.5", "v1.34.0", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	ncID, _, err := s.AddClusterNodeConfig(canonical, cid, []byte("enc"), "sha", "generated", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := SetHostCluster(mac, &cid); err != nil {
		t.Fatalf("SetHostCluster: %v", err)
	}
	if err := SetHostMachineType(mac, "controlplane"); err != nil {
		t.Fatalf("SetHostMachineType: %v", err)
	}
	if err := SetHostNodeConfig(mac, &ncID); err != nil {
		t.Fatalf("SetHostNodeConfig: %v", err)
	}

	h, err := GetMacAddress(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if h.ClusterID == nil || *h.ClusterID != cid || h.MachineType != "controlplane" ||
		h.NodeConfigID == nil || *h.NodeConfigID != ncID {
		t.Fatalf("wrapper round-trip = %+v", h)
	}

	// Invalid MAC rejected before any store touch.
	if err := SetHostCluster("not-a-mac", &cid); err == nil {
		t.Fatal("invalid MAC accepted")
	}
}
