package ostype

import _ "embed"

// debianCDKeyring is the armored Debian CD signing public key, vendored from
// the Debian keyserver network (fingerprint DF9B9C49EAA9298432589D76DA87E80D6294BE9B,
// "Debian CD signing key <debian-cd@lists.debian.org>"). It is a compile-time
// artifact: rotation is fixed by a booty release that re-embeds the new key
// (no hot reload). Verified against real SHA256SUMS/SHA256SUMS.sign for
// releases 11, 12, and 13 — a single key covers all three.
//
//go:embed keys/debian-cd.asc
var debianCDKeyring []byte

// DebianCDKeyring returns the armored Debian CD signing public keyring used to
// verify cdimage SHA256SUMS.sign. Mirrors the flatcarKeyring consumption
// pattern (flatcar_key.go).
func DebianCDKeyring() []byte { return debianCDKeyring }
