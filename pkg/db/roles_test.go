package db

import (
	"errors"
	"testing"
)

func TestCreateGetRole(t *testing.T) {
	s := newTestStore(t)
	cid, _ := s.CreateConfig("role-default", "butane")
	id, err := s.CreateRole("controlplane", &cid)
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	r, err := s.GetRole(id)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if r.Name != "controlplane" || !r.DefaultConfigID.Valid || r.DefaultConfigID.Int64 != cid {
		t.Fatalf("GetRole = %+v, mismatch", r)
	}
	if _, err := s.GetRole(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRole(missing) = %v, want ErrNotFound", err)
	}
}

func TestUpdateRolePartial(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateRole("worker", nil)
	newName := "worker-pool"
	if err := s.UpdateRole(id, &newName, nil); err != nil {
		t.Fatalf("UpdateRole name: %v", err)
	}
	cid, _ := s.CreateConfig("c", "butane")
	if err := s.UpdateRole(id, nil, &cid); err != nil {
		t.Fatalf("UpdateRole default: %v", err)
	}
	r, _ := s.GetRole(id)
	if r.Name != "worker-pool" || r.DefaultConfigID.Int64 != cid {
		t.Fatalf("UpdateRole result = %+v", r)
	}
}

func TestListRolesHostCount(t *testing.T) {
	s := newTestStore(t)
	rid, _ := s.CreateRole("r1", nil)
	s.CreateRole("r2", nil)
	mustHost(t, s, "aa:bb:cc:dd:ee:01")
	if err := s.SetHostRoles("aa:bb:cc:dd:ee:01", []int64{rid}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListRoles()
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ListRoles = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Name == "r1" && r.HostCount != 1 {
			t.Fatalf("r1 HostCount = %d, want 1", r.HostCount)
		}
		if r.Name == "r2" && r.HostCount != 0 {
			t.Fatalf("r2 HostCount = %d, want 0", r.HostCount)
		}
	}
}

func TestSetHostRolesReplacesAndOrders(t *testing.T) {
	s := newTestStore(t)
	mustHost(t, s, "aa:bb:cc:dd:ee:02")
	zeb, _ := s.CreateRole("zebra", nil)
	alp, _ := s.CreateRole("alpha", nil)
	if err := s.SetHostRoles("aa:bb:cc:dd:ee:02", []int64{zeb, alp}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListHostRoles("aa:bb:cc:dd:ee:02")
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zebra" {
		t.Fatalf("ListHostRoles = %+v, want [alpha zebra] (name asc)", got)
	}
	// Replace with only alpha.
	if err := s.SetHostRoles("aa:bb:cc:dd:ee:02", []int64{alp}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListHostRoles("aa:bb:cc:dd:ee:02")
	if len(got) != 1 || got[0].Name != "alpha" {
		t.Fatalf("after replace = %+v, want [alpha]", got)
	}
	// Empty set unbinds all.
	if err := s.SetHostRoles("aa:bb:cc:dd:ee:02", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListHostRoles("aa:bb:cc:dd:ee:02"); len(got) != 0 {
		t.Fatalf("after unbind = %+v, want empty", got)
	}
}

func TestSetHostConfigAndClear(t *testing.T) {
	s := newTestStore(t)
	mustHost(t, s, "aa:bb:cc:dd:ee:03")
	cid, _ := s.CreateConfig("c", "butane")
	if err := s.SetHostConfig("aa:bb:cc:dd:ee:03", &cid); err != nil {
		t.Fatal(err)
	}
	h, _ := s.GetHost("aa:bb:cc:dd:ee:03")
	if h.ConfigID == nil || *h.ConfigID != cid {
		t.Fatalf("host config_id = %v, want %d", h.ConfigID, cid)
	}
	if err := s.SetHostConfig("aa:bb:cc:dd:ee:03", nil); err != nil {
		t.Fatal(err)
	}
	h, _ = s.GetHost("aa:bb:cc:dd:ee:03")
	if h.ConfigID != nil {
		t.Fatalf("host config_id = %v, want nil after clear", h.ConfigID)
	}
}

func mustHost(t *testing.T, s *Store, mac string) {
	t.Helper()
	if err := s.UpsertHost(Host{MAC: mac}); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
}
