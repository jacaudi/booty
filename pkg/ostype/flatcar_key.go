package ostype

import _ "embed"

// flatcarKeyring is the armored Flatcar image-signing public key, vendored from
// https://www.flatcar.org/security/image-signing-key/Flatcar_Image_Signing_Key.asc.
// It is a compile-time artifact: rotation/expiry is fixed by a booty release
// that re-embeds the new key (no hot reload). Primary key
// F88CFEDEFF29A5B4D9523864E25D9AED0593B34A never expires; the active signing
// subkey 52F145DFD00BBDCD928CBB5A32DA80F91EF52974 expires 2027-03-08 — refresh
// before then (CONFIGURATION.md rotation runbook).
//
//go:embed keys/flatcar.asc
var flatcarKeyring []byte
