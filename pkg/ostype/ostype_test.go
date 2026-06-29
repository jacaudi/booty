package ostype

import (
	"slices"
	"testing"
)

func TestFamilyByName(t *testing.T) {
	cases := map[string]struct{ kind, template string }{
		"ignition": {"ignition", "ignition.config.url="},
		"talos":    {"machineconfig", "talos.config="},
		"debian":   {"preseed", "auto url="},
	}
	for name, want := range cases {
		f, ok := FamilyByName(name)
		if !ok {
			t.Errorf("FamilyByName(%q): not found", name)
			continue
		}
		if f.Name != name || f.ConfigKind != want.kind || f.Template != want.template {
			t.Errorf("FamilyByName(%q) = %+v, want kind=%s template=%s", name, f, want.kind, want.template)
		}
	}
	if _, ok := FamilyByName("nope"); ok {
		t.Error("FamilyByName(nope): ok = true, want false")
	}
}

func TestRegistry_RegistersAllFour(t *testing.T) {
	want := []string{"debian", "fedora-coreos", "flatcar", "talos"}
	names := make([]string, 0, len(All()))
	for _, o := range All() {
		names = append(names, o.Name())
	}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Errorf("registered OS = %v, want %v", names, want)
	}
}

func TestLookup_FamilyWiring(t *testing.T) {
	cases := map[string]string{
		"flatcar": "ignition", "fedora-coreos": "ignition",
		"talos": "talos", "debian": "debian",
	}
	for osName, fam := range cases {
		o, ok := Lookup(osName)
		if !ok {
			t.Errorf("Lookup(%q): not found", osName)
			continue
		}
		if o.Family().Name != fam {
			t.Errorf("%s.Family() = %q, want %q", osName, o.Family().Name, fam)
		}
	}
}
