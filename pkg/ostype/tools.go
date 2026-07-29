package ostype

import "slices"

// Tool registrations. Each is DATA: a name plus the arch->endpoints.yml key
// map. Everything else lives in netbootxyzOS, so adding a tool is additive —
// this file, catalogArches (which derives from ToolArches below), the boot
// script in pkg/tftp, and the osTitle label.
func init() {
	register(netbootxyzOS{
		name:      "systemrescue",
		endpoints: map[string]string{"amd64": "systemrescue-amd64"},
	})
	// "uefishell", NOT "uefi-shell-x64". Both endpoints ship the same
	// uefi-shell-x64.efi, but netboot.xyz's own menus reference uefishell;
	// nothing upstream references uefi-shell-x64, making it the likelier of the
	// two to be pruned (which would surface as "endpoint not in manifest").
	// uefishell also carries aarch64/arm, opening arm64 later for free.
	register(netbootxyzOS{
		name:      "uefi-shell",
		endpoints: map[string]string{"amd64": "uefishell"},
	})
	// Replaces Memtest86 (free), whose endpoint is enabled:false upstream and
	// appears only in the ARM menu. This endpoint ships seven files; exactly one
	// (mt86p_x86_64) is booted, under BOTH firmwares.
	register(netbootxyzOS{
		name:      "memtest86plus",
		endpoints: map[string]string{"amd64": "memtest86plus"},
	})
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
