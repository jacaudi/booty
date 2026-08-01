package ostype

import "slices"

// Tool registrations. Each is DATA: a name plus the arch->endpoints.yml key
// map. Everything else lives in netbootxyzOS, so adding a tool is additive —
// this file, catalogArches (which derives from ToolArches below), the boot
// script in pkg/tftp, and the osTitle label.
func init() {
	// files lists exactly the filenames pkg/tftp/tool_scripts.go's
	// systemrescue.ipxe references: vmlinuz and initrd on their `kernel`/
	// `initrd` lines, archiso_pxe_http on its second `initrd` line, and
	// airootfs.sfs which the archiso_pxe_http hook fetches implicitly via the
	// archiso_http_srv token (see that script's comment). All four of the
	// entry's files are used — the allowlist changes no behaviour here, it
	// just makes "what this tool needs" a single explicit statement instead
	// of "every file the manifest happens to list."
	register(netbootxyzOS{
		name:      "systemrescue",
		endpoints: map[string]string{"amd64": "systemrescue-amd64"},
		files:     []string{"vmlinuz", "initrd", "archiso_pxe_http", "airootfs.sfs"},
	})
	// "uefishell", NOT "uefi-shell-x64". Both endpoints ship the same
	// uefi-shell-x64.efi, but netboot.xyz's own menus reference uefishell;
	// nothing upstream references uefi-shell-x64, making it the likelier of the
	// two to be pruned (which would surface as "endpoint not in manifest").
	// uefishell also carries aarch64/arm, opening arm64 later for free.
	//
	// files is the single file pkg/tftp/tool_scripts.go's uefi-shell.ipxe
	// boots (uefi-shell-x64.efi); the endpoint's other two files
	// (uefi-shell-arm.efi, uefi-shell-aarch64.efi) are unbootable on this
	// amd64-only registration and would otherwise be cached for nothing.
	register(netbootxyzOS{
		name:      "uefi-shell",
		endpoints: map[string]string{"amd64": "uefishell"},
		files:     []string{"uefi-shell-x64.efi"},
	})
	// Replaces Memtest86 (free), whose endpoint is enabled:false upstream and
	// appears only in the ARM menu. This endpoint ships seven files; exactly one
	// (mt86p_x86_64) is booted, under BOTH firmwares.
	//
	// files is that one file, per pkg/tftp/tool_scripts.go's
	// memtest86plus.ipxe. netboot.xyz's own asset mirror does not even publish
	// four of the other six at release 8.00-32a14678 (they 404) — see
	// netbootxyzOS.files for why an unfiltered fetch is worse than just wrong.
	register(netbootxyzOS{
		name:      "memtest86plus",
		endpoints: map[string]string{"amd64": "memtest86plus"},
		files:     []string{"mt86p_x86_64"},
	})
	// Debian-stable of the four Clonezilla endpoints upstream publishes
	// (debian/ubuntu x stable/testing) — D11. Tools are param-less, so the
	// choice cannot be a spec key; the Ubuntu and testing variants remain
	// additive later as separate registrations.
	register(netbootxyzOS{
		name:      "clonezilla",
		endpoints: map[string]string{"amd64": "clonezilla-debian-stable-amd64"},
		files:     []string{"vmlinuz", "initrd", "filesystem.squashfs"},
	})
	// Upstream declares no arch for this endpoint; booty registers it under
	// amd64 like every other tool. Its manifest `version` is the literal string
	// "current" forever — the on-disk version is the release tag (D7), which is
	// exactly the case D7 exists for.
	register(netbootxyzOS{
		name:      "rescatux",
		endpoints: map[string]string{"amd64": "rescatux"},
		files:     []string{"vmlinuz", "initrd", "filesystem.squashfs"},
	})
	// Upstream declares no arch and publishes exactly one file: the RECOVERY
	// EFI image. It appears only under utilitiesefi, so it is EFI-only.
	register(netbootxyzOS{
		name:      "zfsbootmenu",
		endpoints: map[string]string{"amd64": "zfsbootmenu"},
		files:     []string{"zfsbootmenu-recovery-x86_64.efi"},
	})
	// Upstream key is shredos-x86_64 (its manifest arch is x86_64); booty
	// registers it under amd64 like every other tool — the endpoints map exists
	// to absorb exactly this. The 32-bit shredos-i686 endpoint is not offered.
	register(netbootxyzOS{
		name:      "shredos",
		endpoints: map[string]string{"amd64": "shredos-x86_64"},
		files:     []string{"shredos"},
	})
	// The ISO is 1.9 GB and mounted as a third initrd via fromiso=. It is marked
	// large so it lands through the resumable downloader (D13) — the staged path's
	// 5-minute whole-request ceiling cannot fetch it on most links.
	//
	// 9990-misc-helpers.sh is netboot.xyz's PATCH, not an extra: netbootxyz#1624
	// was Tails failing to mount the ISO because the loop module was not loaded,
	// and the modprobe fix ships in this helper. It is version-coupled to the
	// ISO; both share the release tag, so D7 keeps them in step.
	register(netbootxyzOS{
		name:      "tails",
		endpoints: map[string]string{"amd64": "tails"},
		files:     []string{"vmlinuz", "initrd.img", "9990-misc-helpers.sh", "tails-amd64.iso"},
		large:     map[string]bool{"tails-amd64.iso": true},
	})
}

// ToolFiles reports each registered tool's file allowlist. Same shape and
// purpose as ToolArches: the single source pkg/tftp uses to verify its boot
// scripts reference no file outside what booty actually caches, and vice
// versa, so the two data sets cannot silently drift.
func ToolFiles() map[string][]string {
	out := map[string][]string{}
	for _, o := range All() {
		tool, ok := o.(netbootxyzOS)
		if !ok {
			continue
		}
		out[tool.name] = slices.Clone(tool.files)
	}
	return out
}

// ToolArches reports each registered tool's supported arches. It is the SINGLE
// source for that knowledge: pkg/cache's catalogArches derives its tool rows
// from this rather than repeating them, so the two cannot drift.
func ToolArches() map[string][]string {
	out := map[string][]string{}
	for _, o := range All() {
		tool, ok := o.(netbootxyzOS)
		if !ok {
			continue
		}
		arches := make([]string, 0, len(tool.endpoints))
		for a := range tool.endpoints {
			arches = append(arches, a)
		}
		slices.Sort(arches)
		out[tool.name] = arches
	}
	return out
}
