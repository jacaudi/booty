package tftp

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/ostype"
)

func TestToolScriptsRegistered(t *testing.T) {
	// These scripts re-chain booty.ipxe from a guard branch so the branch
	// TERMINATES rather than falling through into whatever label follows.
	// That is the opposite of booting via chain — no tool boots its payload
	// with chain, which is what this guard actually protects.
	rechains := map[string]bool{
		"uefi-shell.ipxe":  true, // EFI-only guard
		"zfsbootmenu.ipxe": true, // EFI-only guard
		"shredos.ipxe":     true, // safe arm of the destructive-confirm gate
	}
	// Derived from the registry, not a literal list: a new tool registered
	// without a script must FAIL here, and a hardcoded list would just not
	// look for it.
	for name := range ostype.ToolFiles() {
		k := name + ".ipxe"
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
		if strings.Contains(s, "chain ") && !rechains[k] {
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

func TestRescatuxMatchesOracle(t *testing.T) {
	s := PXEConfig["rescatux.ipxe"]
	if got := strings.Count(s, "\ninitrd "); got != 1 {
		t.Errorf("initrd lines = %d, want 1\n%s", got, s)
	}
	for _, want := range []string{
		"[[baseurl]]/vmlinuz",
		"boot=live",
		"fetch=[[baseurl]]/filesystem.squashfs",
		"selinux=1",
		"security=selinux",
		"enforcing=0",
		"initrd=initrd.magic", // {{ kernel_params }}
		"\ninitrd [[baseurl]]/initrd",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	// Upstream lists rescatux under BOTH utilitiesefi and utilitiespcbios64.
	if strings.Contains(s, "${platform}") {
		t.Errorf("rescatux must not branch on platform:\n%s", s)
	}
	// Guard against the derivation mistake: these are Clonezilla-only options.
	for _, forbidden := range []string{"ocs_live_run", "union=overlay", "username=user"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("Clonezilla-only option %q leaked into rescatux:\n%s", forbidden, s)
		}
	}
}

func TestShredOSGateIsSafe(t *testing.T) {
	s := PXEConfig["shredos.ipxe"]

	// 1. The destructive kernel/boot pair must be LAST — nothing may fall into it.
	trimmed := strings.TrimRight(s, "\n")
	lines := strings.Split(trimmed, "\n")
	if n := len(lines); n < 2 ||
		!strings.HasPrefix(lines[n-2], "kernel ") || lines[n-1] != "boot" {
		t.Fatalf("the kernel/boot pair must be the final two lines so nothing falls into the wipe:\n%s", s)
	}

	// 2. The choose must default to the SAFE arm and carry a timeout.
	if !strings.Contains(s, "--default back") {
		t.Errorf("choose must default to the safe arm:\n%s", s)
	}
	if !strings.Contains(s, "--timeout 300000") {
		t.Errorf("choose must carry booty's standard timeout:\n%s", s)
	}

	// 3. The wipe method is pinned.
	if !strings.Contains(s, `nwipe_options="--method=prng"`) {
		t.Errorf("wipe method must be prng:\n%s", s)
	}

	// 4. The gate must warn, without overstating: ShredOS does NOT autonuke at
	// launch, so the wording says "erases disks irreversibly", not "wipes on boot".
	if !strings.Contains(strings.ToLower(s), "irreversib") {
		t.Errorf("gate must warn that erasure is irreversible:\n%s", s)
	}

	// 5. Kernel-only: ShredOS boots a single image with no initrd.
	if got := strings.Count(s, "\ninitrd "); got != 0 {
		t.Errorf("initrd lines = %d, want 0\n%s", got, s)
	}
	// 6. Upstream uses bare ${cmdline}, NOT kernel_params — so no initrd.magic.
	if strings.Contains(s, "initrd.magic") {
		t.Errorf("ShredOS is kernel-only; initrd=initrd.magic must not appear:\n%s", s)
	}
}

func TestTailsMatchesOracle(t *testing.T) {
	s := PXEConfig["tails.ipxe"]
	if got := strings.Count(s, "\ninitrd "); got != 3 {
		t.Fatalf("initrd lines = %d, want 3\n%s", got, s)
	}
	for _, want := range []string{
		"[[baseurl]]/vmlinuz",
		"boot=live",
		"fromiso=/tails.iso",
		"initrd=initrd.magic", // {{ kernel_params }}
		"\ninitrd [[baseurl]]/initrd.img",
		"\ninitrd [[baseurl]]/9990-misc-helpers.sh /usr/lib/live/boot/9990-misc-helpers.sh",
		"\ninitrd [[baseurl]]/tails-amd64.iso /tails.iso",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	// Upstream writes tails-${os_arch}.iso; booty's literal must be resolved.
	if strings.Contains(s, "${os_arch}") {
		t.Errorf("os_arch must be resolved to amd64:\n%s", s)
	}
}

func TestZFSBootMenuGuardsBIOSTerminally(t *testing.T) {
	s := PXEConfig["zfsbootmenu.ipxe"]
	if !strings.Contains(s, "${platform}") {
		t.Errorf("must branch on platform (EFI-only upstream):\n%s", s)
	}
	if !strings.Contains(s, "[[baseurl]]/zfsbootmenu-recovery-x86_64.efi") {
		t.Errorf("must boot the recovery EFI:\n%s", s)
	}
	if got := strings.Count(s, "\ninitrd "); got != 0 {
		t.Errorf("initrd lines = %d, want 0 (direct EFI chainload)\n%s", got, s)
	}
	// The guard branch must END the script, not fall through.
	if !strings.Contains(s, "chain tftp://[[server-ip]]/booty.ipxe") {
		t.Errorf("BIOS guard must re-chain rather than fall through:\n%s", s)
	}
	// The guard must be the LAST block, so nothing can fall into the boot path.
	if strings.Index(s, ":notefi") < strings.Index(s, "kernel ") {
		t.Errorf(":notefi must come after the kernel line:\n%s", s)
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
	if len(toolFiles) != 8 {
		t.Fatalf("ToolFiles() returned %d tools, want 8 (systemrescue, uefi-shell, memtest86plus, clonezilla, rescatux, zfsbootmenu, shredos, tails)", len(toolFiles))
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

		// The OTHER direction, which the loop above cannot see. A script that
		// fetches a file the allowlist omits compiles, passes every other
		// test, and then 404s at BOOT time — Artifacts never caches the file,
		// so [[baseurl]]/<it> is not on disk. That is the expensive failure
		// mode (a lab boot or a user's bare metal finds it), so it is asserted
		// here rather than promised in prose.
		for _, ref := range baseurlRefs(script) {
			if !slices.Contains(files, ref) {
				t.Errorf("%s: PXEConfig[%q] fetches [[baseurl]]/%s but %q is NOT in the allowlist — "+
					"booty will not cache it and the boot will 404. Add it to files in pkg/ostype/tools.go.",
					name, name+".ipxe", ref, ref)
			}
		}
	}
}

// baseurlRefs returns every filename an iPXE script fetches as
// "[[baseurl]]/<file>". A reference ends at the first whitespace or quote —
// clonezilla.ipxe's "fetch=[[baseurl]]/filesystem.squashfs initrd=..." puts a
// space immediately after the name, and nothing in these scripts quotes a URL.
func baseurlRefs(script string) []string {
	const marker = "[[baseurl]]/"
	var out []string
	for rest := script; ; {
		i := strings.Index(rest, marker)
		if i < 0 {
			return out
		}
		rest = rest[i+len(marker):]
		end := strings.IndexAny(rest, " \t\n\"'")
		if end < 0 {
			end = len(rest)
		}
		if name := rest[:end]; name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
		rest = rest[end:]
	}
}
