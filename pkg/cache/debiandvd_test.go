package cache

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// newTestPGPEntity generates a throwaway keypair for signing test fixtures.
func newTestPGPEntity(t *testing.T) *openpgp.Entity {
	t.Helper()
	ent, err := openpgp.NewEntity("debian-dvd-test", "", "t@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	return ent
}

// writeDetachedSig binary-detach-signs signedPath with ent and writes the
// signature to sigPath, mirroring the on-disk SHA256SUMS.sign layout.
func writeDetachedSig(t *testing.T, signedPath, sigPath string, ent *openpgp.Entity) {
	t.Helper()
	body, err := os.ReadFile(signedPath)
	if err != nil {
		t.Fatal(err)
	}
	var sig bytes.Buffer
	if err := openpgp.DetachSign(&sig, ent, bytes.NewReader(body), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sigPath, sig.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// swapDebianKeyring injects a fixture keyring in place of the embedded Debian
// CD key for the duration of a test, returning a restore func.
func swapDebianKeyring(key []byte) func() {
	old := debianCDKeyring
	debianCDKeyring = key
	return func() { debianCDKeyring = old }
}

func TestVerifyDVDChecksums_HappyAndTamper(t *testing.T) {
	dir := t.TempDir()
	iso := []byte("pretend-iso-contents")
	if err := os.WriteFile(filepath.Join(dir, "debian-12.15.0-amd64-DVD-1.iso"), iso, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(iso)
	sums := fmt.Sprintf("%x  debian-12.15.0-amd64-DVD-1.iso\n", sum[:])
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	// sign SHA256SUMS with a throwaway entity; write SHA256SUMS.sign; inject the
	// entity's public key via the keyring seam.
	ent := newTestPGPEntity(t)
	writeDetachedSig(t, filepath.Join(dir, "SHA256SUMS"), filepath.Join(dir, "SHA256SUMS.sign"), ent)
	restore := swapDebianKeyring(armorPublicKey(t, ent))
	defer restore()

	names := []string{"debian-12.15.0-amd64-DVD-1.iso"}
	if err := verifyDVDChecksums(t.Context(), dir, names); err != nil {
		t.Fatalf("valid set should verify: %v", err)
	}
	// tamper the ISO -> checksum mismatch
	if err := os.WriteFile(filepath.Join(dir, names[0]), append(iso, 'X'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyDVDChecksums(t.Context(), dir, names); err == nil {
		t.Fatal("tampered ISO must fail checksum")
	}
}
