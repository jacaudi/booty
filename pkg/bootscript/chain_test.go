package bootscript

import (
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/ostype"
)

func TestChainPathCarriesArch(t *testing.T) {
	const want = "/booty.ipxe?mac=${mac}&arch=${buildarch}"
	if ChainPath != want {
		t.Errorf("ChainPath = %q, want %q", ChainPath, want)
	}
}

func TestEveryChainLineCarriesArch(t *testing.T) {
	cases := []struct{ name, s string }{
		{"StubScript", StubScript("h:1")},
		{"holding.ipxe", PXEConfig["holding.ipxe"]},
		{"uefi-shell.ipxe", PXEConfig["uefi-shell.ipxe"]},
		{"zfsbootmenu.ipxe", PXEConfig["zfsbootmenu.ipxe"]},
		{"shredos.ipxe", PXEConfig["shredos.ipxe"]},
		{"renderMenu", renderMenu(nil, nil, "h:1")},
	}
	for _, tc := range cases {
		if !strings.Contains(tc.s, ChainPath) {
			t.Errorf("%s does not contain ChainPath %q:\n%s", tc.name, ChainPath, tc.s)
		}
		if strings.Contains(tc.s, "booty.ipxe?mac=${mac} ") {
			t.Errorf("%s still chains without the arch:\n%s", tc.name, tc.s)
		}
	}
}

func TestAssignable(t *testing.T) {
	want := map[string]bool{"talos": true, "flatcar": true, "coreos": true, "debian": true}
	names := []string{"coreos", "holding"}
	for _, o := range ostype.All() {
		names = append(names, o.Name())
	}
	for _, name := range names {
		if got := Assignable(name); got != want[name] {
			t.Errorf("Assignable(%q) = %v, want %v", name, got, want[name])
		}
	}
}
