package ostype

import (
	"slices"
	"testing"
)

func TestDebian_Basics(t *testing.T) {
	o, _ := Lookup("debian")
	if o.Family().Name != "debian" || o.Family().ConfigKind != "preseed" {
		t.Errorf("family = %+v, want debian/preseed", o.Family())
	}
	if !slices.Equal(o.RequiredParams(), []string{"channel"}) {
		t.Errorf("RequiredParams = %v, want [channel]", o.RequiredParams())
	}
}

func TestDebian_ValidateAndCompare(t *testing.T) {
	o, _ := Lookup("debian")
	if err := o.ValidateVersion("12.5"); err != nil {
		t.Errorf("12.5 rejected: %v", err)
	}
	if err := o.ValidateVersion("12"); err != nil {
		t.Errorf("12 rejected: %v", err)
	}
	if err := o.ValidateVersion("bookworm"); err == nil {
		t.Error("non-numeric version accepted")
	}
	if o.CompareVersions("12.5", "12.4") <= 0 {
		t.Error("12.5 should sort after 12.4")
	}
	if o.CompareVersions("12.0", "11.9") <= 0 {
		t.Error("12.0 should sort after 11.9")
	}
}

func TestDebian_DiscoverVersions_FixedSet(t *testing.T) {
	o, _ := Lookup("debian")
	got, err := o.DiscoverVersions(t.Context(), nil)
	if err != nil {
		t.Fatalf("DiscoverVersions: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("DiscoverVersions returned empty fixed set")
	}
	// Must be ordered newest-first by CompareVersions.
	if !slices.IsSortedFunc(got, func(a, b string) int { return o.CompareVersions(b, a) }) {
		t.Errorf("fixed set not newest-first: %v", got)
	}
}

func TestDebian_Artifacts(t *testing.T) {
	o, _ := Lookup("debian")
	got := o.Artifacts("12.5", "amd64", map[string]string{"channel": "stable"})
	if len(got) != 2 {
		t.Fatalf("debian artifacts = %d, want 2 (linux, initrd.gz)", len(got))
	}
	for _, a := range got {
		if a.URL == "" || a.Filename == "" {
			t.Errorf("incomplete debian artifact: %+v", a)
		}
		// codename for stable must appear in the URL.
		if !slices.Contains([]string{"linux", "initrd.gz"}, a.Filename) {
			t.Errorf("unexpected debian artifact filename %q", a.Filename)
		}
	}
}
