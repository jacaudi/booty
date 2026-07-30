package tftp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/ostype"
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

// TestToolFileAllowlistTracksBootScripts ties ostype's per-tool file allowlist
// (pkg/ostype/tools.go) to the boot scripts in this file, so the two cannot
// silently drift: an allowlist entry a script no longer needs, or a script
// dependency the allowlist forgot, both fail this test.
//
// It checks the WHOLE source of tool_scripts.go, not just each PXEConfig map
// value: the archiso_pxe_http hook fetches airootfs.sfs implicitly via the
// archiso_http_srv token (see the comment above
// PXEConfig["systemrescue.ipxe"]), so that filename is documented next to the
// script rather than present in its literal iPXE text.
func TestToolFileAllowlistTracksBootScripts(t *testing.T) {
	toolFiles := ostype.ToolFiles()
	if len(toolFiles) != 3 {
		t.Fatalf("ToolFiles() returned %d tools, want 3 (systemrescue, uefi-shell, memtest86plus)", len(toolFiles))
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "tool_scripts.go"))
	if err != nil {
		t.Fatalf("read tool_scripts.go: %v", err)
	}
	text := string(src)

	for name, files := range toolFiles {
		if len(files) == 0 {
			t.Errorf("%s: file allowlist is empty", name)
		}
		if _, ok := PXEConfig[name+".ipxe"]; !ok {
			t.Errorf("%s: no PXEConfig entry %q", name, name+".ipxe")
		}
		for _, f := range files {
			if !strings.Contains(text, f) {
				t.Errorf("%s: allowlisted file %q not referenced anywhere in tool_scripts.go", name, f)
			}
		}
	}
}
