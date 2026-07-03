package ostype

import (
	"bytes"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// TestFlatcarKeyringParsesAndActiveSubkeyNotNearExpiry pins the embedded key:
// it must parse as an armored keyring, carry the active Flatcar signing subkey
// 52F145DF…1EF52974, and that subkey must not expire before 2027-01-01 (the
// plan-time expiry check — a key expiring inside the support horizon is a red
// flag). Rotation is a booty-release bump; see CONFIGURATION.md.
func TestFlatcarKeyringParsesAndActiveSubkeyNotNearExpiry(t *testing.T) {
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(flatcarKeyring))
	if err != nil {
		t.Fatalf("embedded Flatcar key must parse as an armored keyring: %v", err)
	}
	const activeSubkey = "52F145DFD00BBDCD928CBB5A32DA80F91EF52974"
	horizon := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	var found bool
	for _, e := range ring {
		for _, sk := range e.Subkeys {
			if sk.PublicKey.KeyIdString() != activeSubkey[len(activeSubkey)-16:] {
				continue
			}
			found = true
			if sk.Sig == nil || sk.Sig.KeyLifetimeSecs == nil {
				continue // never-expiring subkey is acceptable
			}
			exp := sk.PublicKey.CreationTime.Add(time.Duration(*sk.Sig.KeyLifetimeSecs) * time.Second)
			if exp.Before(horizon) {
				t.Fatalf("active signing subkey expires %s, before the 2027-01-01 horizon — refresh the embedded key", exp)
			}
		}
	}
	if !found {
		t.Fatalf("active signing subkey %s not present in the embedded keyring", activeSubkey)
	}
}
