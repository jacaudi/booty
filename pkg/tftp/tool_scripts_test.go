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
	for _, k := range []string{"systemrescue.ipxe", "uefi-shell.ipxe", "memtest86plus.ipxe", "clonezilla.ipxe"} {
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

func TestClonezillaMatchesOracle(t *testing.T) {
	s := PXEConfig["clonezilla.ipxe"]
	if !strings.HasPrefix(s, "#!ipxe\n") {
		t.Fatal("missing shebang")
	}
	if got := strings.Count(s, "\ninitrd "); got != 1 {
		t.Errorf("initrd lines = %d, want 1\n%s", got, s)
	}
	for _, want := range []string{
		"[[baseurl]]/vmlinuz",
		"boot=live",
		`ocs_live_run="ocs-live-general"`,
		"ocs_live_batch=no",
		"net.ifnames=0",
		"fetch=[[baseurl]]/filesystem.squashfs",
		"initrd=initrd.magic", // {{ kernel_params }} expands to this; absence = no initrd on old UEFI iPXE
		"\ninitrd [[baseurl]]/initrd",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	// Clonezilla is not firmware-gated upstream (it appears in both the EFI and
	// pcbios menus), so it must NOT carry a platform guard.
	if strings.Contains(s, "${platform}") {
		t.Errorf("clonezilla must not branch on platform:\n%s", s)
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

// wholeFileAllowlistException is the ONE filename permitted to satisfy
// TestToolFileAllowlistTracksBootScripts via a whole-file search instead of
// its own tool's actual PXEConfig script value: the archiso_pxe_http hook
// fetches airootfs.sfs implicitly via the archiso_http_srv=[[baseurl]] token
// (see the comment above PXEConfig["systemrescue.ipxe"]), so it is never
// written literally into the script — only named in the comment beside it.
// Every OTHER allowlisted file must appear in its own script's literal text,
// so a comment mentioning a filename elsewhere in tool_scripts.go (this file
// says "initrd" 15 times and "mt86p_x86_64" twice, almost all in prose) can
// never substitute for the real boot-script dependency.
const wholeFileAllowlistException = "airootfs.sfs"

// TestToolFileAllowlistTracksBootScripts ties ostype's per-tool file allowlist
// (pkg/ostype/tools.go) to the boot scripts in this file, so the two cannot
// silently drift: an allowlist entry a script no longer needs, or a script
// dependency the allowlist forgot, both fail this test. Renaming a kernel/
// initrd filename in any tool's PXEConfig entry, or removing one of its
// initrd lines, fails this test even though the old name may still appear in
// a nearby comment.
//
// Every script references its files as "[[baseurl]]/<file>" (see the
// [[baseurl]] doc comment atop this file) — matching that exact prefix,
// rather than the bare filename, is required: systemrescue.ipxe's kernel line
// also carries the LITERAL iPXE directive "initrd=initrd.magic" (see its
// comment), which contains the substring "initrd" and would otherwise let a
// bare-filename check pass even after the real "initrd [[baseurl]]/initrd"
// line was deleted.
func TestToolFileAllowlistTracksBootScripts(t *testing.T) {
	toolFiles := ostype.ToolFiles()
	if len(toolFiles) != 4 {
		t.Fatalf("ToolFiles() returned %d tools, want 4 (systemrescue, uefi-shell, memtest86plus, clonezilla)", len(toolFiles))
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "tool_scripts.go"))
	if err != nil {
		t.Fatalf("read tool_scripts.go: %v", err)
	}
	wholeFile := string(src)

	for name, files := range toolFiles {
		script, ok := PXEConfig[name+".ipxe"]
		if !ok {
			t.Errorf("%s: no PXEConfig entry %q", name, name+".ipxe")
			continue
		}
		if len(files) == 0 {
			t.Errorf("%s: file allowlist is empty", name)
		}
		for _, f := range files {
			if f == wholeFileAllowlistException {
				if !strings.Contains(wholeFile, f) {
					t.Errorf("%s: allowlisted file %q not referenced anywhere in tool_scripts.go", name, f)
				}
				continue
			}
			if !strings.Contains(script, "[[baseurl]]/"+f) {
				t.Errorf("%s: allowlisted file %q not referenced as [[baseurl]]/%s in PXEConfig[%q]:\n%s", name, f, f, name+".ipxe", script)
			}
		}
	}
}
