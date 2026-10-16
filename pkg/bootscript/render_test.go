package bootscript

import (
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

func TestStubScript(t *testing.T) {
	want := "#!ipxe\nchain http://10.0.0.1:8080/booty.ipxe?mac=${mac}&arch=${buildarch} || shell\n"
	if got := StubScript("10.0.0.1:8080"); got != want {
		t.Fatalf("StubScript = %q, want %q", got, want)
	}
}

func TestNoTFTPChainsRemain(t *testing.T) {
	for name, tmpl := range PXEConfig {
		if strings.Contains(tmpl, "chain tftp://") {
			t.Errorf("%s still chains over TFTP:\n%s", name, tmpl)
		}
	}
	menu := renderMenu(nil, nil, "10.0.0.1:8080")
	const retryChain = "chain http://10.0.0.1:8080/booty.ipxe?mac=${mac}&arch=${buildarch} || shell"
	if strings.Contains(menu, "tftp://") || !strings.Contains(menu, retryChain) {
		t.Errorf("menu retry chain wrong:\n%s", menu)
	}
}

func TestRenderBootScriptKinds(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.ServerIP, "10.0.0.1")
	viper.Set(config.DataDir, t.TempDir())
	SetStore(nil)
	cases := []struct {
		name   string
		host   *hardware.Host
		kind   string
		marker string
	}{
		{"unknown", nil, "holding", "not yet approved"},
		{"unapproved", &hardware.Host{MAC: "aa:bb:cc:dd:ee:01"}, "holding", "not yet approved"},
		{
			"assigned flatcar",
			&hardware.Host{MAC: "aa:bb:cc:dd:ee:02", Approved: true, BootMode: "assigned", AssignedOS: "flatcar"},
			"assigned", "flatcar_production_pxe.vmlinuz",
		},
		{
			"menu",
			&hardware.Host{MAC: "aa:bb:cc:dd:ee:03", Approved: true, BootMode: "menu"},
			"menu", "menu Booty - select an image to boot",
		},
	}
	for _, c := range cases {
		script, kind := RenderBootScript(c.host, "10.0.0.1:8080")
		if kind != c.kind || !strings.Contains(script, c.marker) {
			t.Errorf("%s: kind=%q, want %q; marker %q missing:\n%s", c.name, kind, c.kind, c.marker, script)
		}
	}
}
