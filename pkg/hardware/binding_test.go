package hardware

import (
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

func bindingStore(t *testing.T) *db.Store {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	s, err := db.Open(filepath.Join(t.TempDir(), "booty.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { SetStore(nil); s.Close() })
	SetStore(s)
	return s
}

func TestHardwareSetHostConfig(t *testing.T) {
	s := bindingStore(t)
	const mac = "aa:bb:cc:dd:ee:10"
	if err := WriteMacAddress(mac, Host{}); err != nil {
		t.Fatal(err)
	}
	cid, _ := s.CreateConfig("c", "butane")
	if err := SetHostConfig(mac, &cid); err != nil {
		t.Fatalf("SetHostConfig: %v", err)
	}
	h, _ := GetMacAddress(mac)
	if h.ConfigID == nil || *h.ConfigID != cid {
		t.Fatalf("host ConfigID = %v, want %d", h.ConfigID, cid)
	}
	if err := SetHostConfig(mac, nil); err != nil {
		t.Fatal(err)
	}
	h, _ = GetMacAddress(mac)
	if h.ConfigID != nil {
		t.Fatalf("ConfigID = %v, want nil after clear", h.ConfigID)
	}
}

func TestHardwareSetHostRoles(t *testing.T) {
	s := bindingStore(t)
	const mac = "aa:bb:cc:dd:ee:11"
	if err := WriteMacAddress(mac, Host{}); err != nil {
		t.Fatal(err)
	}
	r1, _ := s.CreateRole("alpha", nil)
	r2, _ := s.CreateRole("beta", nil)
	if err := SetHostRoles(mac, []int64{r2, r1}); err != nil {
		t.Fatalf("SetHostRoles: %v", err)
	}
	// F6: verify the mutation through the db store read (role reads go via the
	// store, not a hardware wrapper — there is no hardware.ListHostRoles).
	got, err := s.ListHostRoles(mac)
	if err != nil {
		t.Fatalf("store.ListHostRoles: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "beta" {
		t.Fatalf("host roles = %+v, want [alpha beta] (name asc)", got)
	}
}

func TestHardwareSetSchematic(t *testing.T) {
	bindingStore(t)
	const mac = "aa:bb:cc:dd:ee:12"
	if err := WriteMacAddress(mac, Host{OS: "talos"}); err != nil {
		t.Fatal(err)
	}
	if err := SetSchematic(mac, "a1b2c3d4"); err != nil {
		t.Fatalf("SetSchematic: %v", err)
	}
	h, err := GetMacAddress(mac)
	if err != nil || h.Schematic != "a1b2c3d4" {
		t.Fatalf("host schematic = %q (err %v), want a1b2c3d4", h.Schematic, err)
	}
	// Canonicalization: an uppercase/dash MAC reaches the same row.
	if err := SetSchematic("AA-BB-CC-DD-EE-12", "e5f6a7b8"); err != nil {
		t.Fatal(err)
	}
	if h, _ := GetMacAddress(mac); h.Schematic != "e5f6a7b8" {
		t.Fatalf("canonicalized re-bind = %q, want e5f6a7b8", h.Schematic)
	}
	if err := SetSchematic("not-a-mac", "x"); err == nil {
		t.Fatal("invalid MAC must be rejected")
	}
}
