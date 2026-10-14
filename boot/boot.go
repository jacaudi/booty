// Package boot holds booty's embedded iPXE binaries (see THIRD_PARTY_NOTICES.md).
// Rebuild them with `go generate ./boot/` (or `task ipxe`) and commit the outputs;
// plain `go build` never runs the generator.
package boot

//go:generate ./build-ipxe.sh

import (
	"embed"
	"slices"
)

// Files holds the embedded iPXE binaries named by [Names].
//
//go:embed undionly.kpxe ipxe.efi snponly.efi
var Files embed.FS

// Names lists the files in [Files]. Each call returns a fresh slice, so a
// caller cannot change what the package serves.
func Names() []string {
	return []string{"undionly.kpxe", "ipxe.efi", "snponly.efi"}
}

// Lookup returns the embedded bytes of name when it is exactly one of Names.
func Lookup(name string) ([]byte, bool) {
	if !slices.Contains(Names(), name) {
		return nil, false
	}
	b, err := Files.ReadFile(name)
	if err != nil {
		return nil, false
	}
	return b, true
}
