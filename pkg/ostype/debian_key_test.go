package ostype

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// TestDebianCDKeyringParses pins the embedded key: it must parse as an
// armored keyring and carry the Debian CD signing key
// DF9B9C49EAA9298432589D76DA87E80D6294BE9B (verified 2026-07-17 against real
// SHA256SUMS/SHA256SUMS.sign for releases 11.11.0, 12.12.0, and 13.5.0 — see
// commit body). Unlike Flatcar's signing subkey, this primary key carries no
// expiration (`gpg --with-colons --list-keys` reports an empty expiry field),
// so there is no expiry horizon to pin.
func TestDebianCDKeyringParses(t *testing.T) {
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(DebianCDKeyring()))
	if err != nil {
		t.Fatalf("embedded Debian CD key must parse as an armored keyring: %v", err)
	}
	const fingerprint = "DF9B9C49EAA9298432589D76DA87E80D6294BE9B"
	var found bool
	for _, e := range ring {
		if fmt.Sprintf("%X", e.PrimaryKey.Fingerprint) == fingerprint {
			found = true
		}
	}
	if !found {
		t.Fatalf("Debian CD signing key %s not present in the embedded keyring", fingerprint)
	}
}
