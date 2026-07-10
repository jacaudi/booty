package db

import "testing"

func TestSetHostSchematic(t *testing.T) {
	s := newTestStore(t)
	const mac = "aa:bb:cc:dd:ee:40"
	if err := s.UpsertHost(Host{MAC: mac}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostSchematic(mac, "a1b2c3d4"); err != nil {
		t.Fatalf("SetHostSchematic: %v", err)
	}
	h, err := s.GetHost(mac)
	if err != nil || h.Schematic != "a1b2c3d4" {
		t.Fatalf("schematic = %q (err %v), want a1b2c3d4", h.Schematic, err)
	}
	// Re-bind overwrites (D3: hosts roll forward on explicit re-bind).
	if err := s.SetHostSchematic(mac, "e5f6a7b8"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.GetHost(mac); h.Schematic != "e5f6a7b8" {
		t.Fatalf("re-bind schematic = %q, want e5f6a7b8", h.Schematic)
	}
}
