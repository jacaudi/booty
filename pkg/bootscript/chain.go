package bootscript

// ArchQueryKey is the query key under which a chain to /booty.ipxe reports the
// iPXE build architecture.
const ArchQueryKey = "arch"

// ChainPath is the path and query of every chain from a booty-rendered script
// back to /booty.ipxe (design D6): the MAC iPXE reports, plus its build arch so
// the server can record what an unknown host is. The one deliberate exception
// is boot/embed.ipxe:4, which is compiled into the embedded iPXE binaries and
// cannot use this constant; its chain carries only the MAC.
const ChainPath = "/booty.ipxe?mac=${mac}&" + ArchQueryKey + "=${buildarch}"

// Assignable reports whether os can be assigned to a host: it has a boot
// template, and is neither the holding script nor a tool OS. It mirrors the
// success condition of assignedScript, which refuses a tool
// OS and has no template to render for an unknown name.
func Assignable(os string) bool {
	return PXEConfig[os+".ipxe"] != "" && os != "holding" && !isToolOS(os)
}
