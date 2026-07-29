package tftp

import (
	"strings"
	"testing"
)

func TestToolScriptsRegistered(t *testing.T) {
	for _, k := range []string{"systemrescue.ipxe", "uefi-shell.ipxe", "memtest86plus.ipxe"} {
		s, ok := PXEConfig[k]
		if !ok {
			t.Errorf("PXEConfig[%q] missing", k)
			continue
		}
		if !strings.HasPrefix(s, "#!ipxe\n") {
			t.Errorf("%s: must start with the #!ipxe shebang", k)
		}
		if !strings.Contains(s, "[[baseurl]]") {
			t.Errorf("%s: must reference the [[baseurl]] token", k)
		}
		if strings.Contains(s, "chain ") && k != "uefi-shell.ipxe" {
			t.Errorf("%s: tools boot via kernel/sanboot, never chain", k)
		}
	}
}

func TestSystemRescueMatchesOracle(t *testing.T) {
	s := PXEConfig["systemrescue.ipxe"]
	if got := strings.Count(s, "\ninitrd "); got != 2 {
		t.Fatalf("initrd lines = %d, want 2\n%s", got, s)
	}
	for _, want := range []string{
		"archiso_pxe_http /hooks/archiso_pxe_http mode=755",
		"archisobasedir=sysresccd",
		"BOOTIF=${netX/mac}",
		"initrd=initrd.magic",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	// The hook appends its own "/airootfs.sfs", so a trailing slash here would
	// produce a double slash that only works via booty's ServeMux 301.
	if strings.Contains(s, "archiso_http_srv=[[baseurl]]/") {
		t.Errorf("archiso_http_srv must NOT have a trailing slash:\n%s", s)
	}
}

func TestMemtest86PlusBootsSingleBinary(t *testing.T) {
	s := PXEConfig["memtest86plus.ipxe"]
	if !strings.Contains(s, "[[baseurl]]/mt86p_x86_64") {
		t.Errorf("must boot mt86p_x86_64:\n%s", s)
	}
	if strings.Contains(s, "${platform}") {
		t.Errorf("no firmware branch is needed on amd64:\n%s", s)
	}
}

func TestUEFIShellGuardsBIOSTerminally(t *testing.T) {
	s := PXEConfig["uefi-shell.ipxe"]
	if !strings.Contains(s, "${platform}") {
		t.Errorf("must branch on platform:\n%s", s)
	}
	if !strings.Contains(s, "[[baseurl]]/uefi-shell-x64.efi") {
		t.Errorf("must boot uefi-shell-x64.efi:\n%s", s)
	}
	// The guard branch must END the script, not fall through into whatever
	// label follows in the generated menu.
	if !strings.Contains(s, "chain tftp://[[server-ip]]/booty.ipxe") {
		t.Errorf("BIOS guard must re-chain rather than fall through:\n%s", s)
	}
}
