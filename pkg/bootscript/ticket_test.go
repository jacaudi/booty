package bootscript

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/bootticket"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/ostype"
)

// ticketRE captures the boot ticket a rendered script carries.
var ticketRE = regexp.MustCompile(`ticket=([A-Za-z0-9._-]+)`)

func scriptTicketer(t *testing.T) *bootticket.Ticketer {
	t.Helper()
	tk, err := bootticket.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestConfigFamily(t *testing.T) {
	for name, want := range map[string]string{
		"talos":         ostype.FamilyTalos,
		"flatcar":       ostype.FamilyIgnition,
		"coreos":        ostype.FamilyIgnition,
		"debian":        ostype.FamilyDebian,
		"memtest86plus": "",
		"no-such-os":    "",
	} {
		if got := configFamily(name); got != want {
			t.Errorf("configFamily(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestAssignedScriptsCarryAValidTicket: every OS that fetches a config gets a
// ticket for its own MAC and family, directly after mac= (design R12).
func TestAssignedScriptsCarryAValidTicket(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	const mac = "aa:bb:cc:dd:ee:51"
	tk := scriptTicketer(t)
	for osName, family := range map[string]string{
		"talos":   ostype.FamilyTalos,
		"flatcar": ostype.FamilyIgnition,
		"coreos":  ostype.FamilyIgnition,
		"debian":  ostype.FamilyDebian,
	} {
		t.Run(osName, func(t *testing.T) {
			host := &hardware.Host{MAC: mac, Approved: true, BootMode: "assigned", AssignedOS: osName}
			got := assignedScript(host, "10.0.0.5:8080", tk)
			if strings.Contains(got, "[[") {
				t.Fatalf("unsubstituted token:\n%s", got)
			}
			if !strings.Contains(got, "?mac=${mac}&ticket=v1.") {
				t.Fatalf("ticket does not directly follow mac=:\n%s", got)
			}
			m := ticketRE.FindStringSubmatch(got)
			if m == nil {
				t.Fatalf("no ticket in:\n%s", got)
			}
			if err := tk.Check(m[1], mac, family); err != nil {
				t.Errorf("ticket does not verify for %s/%s: %v", mac, family, err)
			}
		})
	}
}

// TestTalosTicketPrecedesSerial: a ${serial} that expands to text with a space
// ends the kernel parameter there, so the ticket must come before it (B1).
func TestTalosTicketPrecedesSerial(t *testing.T) {
	tmpl := PXEConfig["talos.ipxe"]
	ticket := strings.Index(tmpl, "ticket=[[ticket]]")
	serial := strings.Index(tmpl, "serial=${serial}")
	hostname := strings.Index(tmpl, "hostname=${hostname}")
	if ticket < 0 || serial < 0 || hostname < 0 || ticket > serial || ticket > hostname {
		t.Fatalf("talos.ipxe must carry ticket= before serial= and hostname=:\n%s", tmpl)
	}
}

// TestScriptWithoutIssuerCarriesEmptyTicket: a nil issuer fails closed (an
// empty ticket the config routes refuse) and never leaves a literal token.
func TestScriptWithoutIssuerCarriesEmptyTicket(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	host := &hardware.Host{MAC: "aa:bb:cc:dd:ee:52", Approved: true, BootMode: "assigned", AssignedOS: "flatcar"}
	got := assignedScript(host, "10.0.0.5:8080", nil)
	if strings.Contains(got, "[[") || strings.Contains(got, "ticket=v1.") {
		t.Fatalf("nil issuer: want an empty ticket and no tokens, got:\n%s", got)
	}
	if !strings.Contains(got, "?mac=${mac}&ticket=") {
		t.Fatalf("nil issuer: the ticket parameter is missing:\n%s", got)
	}
}

func TestMenuSelectionScriptCarriesTicket(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	root := t.TempDir()
	viper.Set(config.DataDir, root)
	if err := os.MkdirAll(filepath.Join(root, "cache", "talos", "schemA", "amd64", "v1.10.5"), 0o755); err != nil {
		t.Fatal(err)
	}
	const mac = "aa:bb:cc:dd:ee:53"
	tk := scriptTicketer(t)
	// A Flatcar host in menu mode that picks Talos: the ticket names the
	// selected OS's family, not the host's.
	host := &hardware.Host{MAC: mac, OS: "flatcar", Approved: true, BootMode: "menu"}
	got := MenuSelectionScript(host, "menu/talos/schemA/amd64/v1.10.5/boot.ipxe", "10.0.0.1", tk)
	m := ticketRE.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("no ticket in:\n%s", got)
	}
	if err := tk.Check(m[1], mac, ostype.FamilyTalos); err != nil {
		t.Fatalf("ticket does not verify for talos: %v", err)
	}
}

func TestHoldingScriptCarriesNoTicket(t *testing.T) {
	if strings.Contains(holdingScript("10.0.0.1"), "ticket=") {
		t.Fatal("the holding script must never carry a ticket")
	}
}
