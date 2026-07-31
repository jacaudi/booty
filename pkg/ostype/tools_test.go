package ostype

import (
	"slices"
	"testing"
)

func TestToolsAreRegistered(t *testing.T) {
	for _, name := range []string{"systemrescue", "uefi-shell", "memtest86plus"} {
		o, ok := Lookup(name)
		if !ok {
			t.Errorf("Lookup(%q) not registered", name)
			continue
		}
		if o.Family().Name != "tool" {
			t.Errorf("%s: family = %q, want tool", name, o.Family().Name)
		}
		if len(o.RequiredParams()) != 0 {
			t.Errorf("%s: RequiredParams = %#v, want empty", name, o.RequiredParams())
		}
	}
}

// DiscoverVersions gets no arch, so it returns the union of tags across arches.
// That is unambiguous only while every tool is single-arch. If this fails, read
// the Interface Limitation section of the plan before "fixing" it: with a
// multi-arch tool one arch would be entirely broken, not merely misordered.
func TestEveryToolIsSingleArch(t *testing.T) {
	for name, arches := range ToolArches() {
		if len(arches) != 1 {
			t.Errorf("%s declares %v; multi-arch tools need the DiscoverVersions arch question answered first", name, arches)
		}
	}
}

func TestToolArchesMatchRegistry(t *testing.T) {
	for name, arches := range ToolArches() {
		o, ok := Lookup(name)
		if !ok {
			t.Fatalf("ToolArches lists unregistered %q", name)
		}
		tool, ok := o.(netbootxyzOS)
		if !ok {
			t.Fatalf("%s is not a netbootxyzOS", name)
		}
		var want []string
		for a := range tool.endpoints {
			want = append(want, a)
		}
		slices.Sort(want)
		got := slices.Clone(arches)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: ToolArches %v != registration %v", name, got, want)
		}
	}
}
