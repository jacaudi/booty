package cache

import (
	"bytes"
	"context"
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

// fakeExtractor returns an isoExtractFunc that, keyed on the ISO's base
// filename, lays down a fixture tree of relative-path -> file-content pairs
// instead of reading a real ISO9660 image. Used by the merge-logic test so it
// needs no real ISO (task-6-brief.md Step 1).
func fakeExtractor(discs map[string]map[string]string) isoExtractFunc {
	return func(_ context.Context, isoPath, destDir string) error {
		files, ok := discs[filepath.Base(isoPath)]
		if !ok {
			return fmt.Errorf("fake extractor: no fixture for %s", isoPath)
		}
		for rel, content := range files {
			full := filepath.Join(destDir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
}

// swapISOExtractor injects a fake isoExtractFunc in place of the real
// diskfs-based extractor for the duration of a test, returning a restore
// func. Mirrors swapDebianKeyring above (same seam strategy).
func swapISOExtractor(fn isoExtractFunc) func() {
	old := isoExtractor
	isoExtractor = fn
	return func() { isoExtractor = old }
}

// assertFile fails the test unless path contains exactly want.
func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func TestExtractAndMergeISO_PoolUnionDisc1Dists(t *testing.T) {
	// fake per-disc extracted contents
	disc1 := map[string]string{
		"dists/bookworm/main/binary-amd64/Packages": "Package: a\n",
		"pool/main/a/a/a_1.deb":                      "AAA",
		"install.amd64/linux":                        "KERNEL",
	}
	disc2 := map[string]string{
		"dists/bookworm/main/binary-amd64/Packages": "SHOULD-NOT-WIN\n", // disc-2 dists ignored
		"pool/main/b/b/b_1.deb":                      "BBB",              // unioned in
	}
	restore := swapISOExtractor(fakeExtractor(map[string]map[string]string{
		"DVD-1.iso": disc1, "DVD-2.iso": disc2,
	}))
	defer restore()

	isoDir := t.TempDir() // where the verbatim ISOs would live (fake extractor keys on name)
	final := filepath.Join(t.TempDir(), "12", "amd64", "12.15.0")
	err := extractAndMergeISO(t.Context(),
		isoDir, []string{"DVD-1.iso", "DVD-2.iso"}, final, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	// disc-1 dists verbatim
	assertFile(t, filepath.Join(final, "dists/bookworm/main/binary-amd64/Packages"), "Package: a\n")
	// pool union
	assertFile(t, filepath.Join(final, "pool/main/a/a/a_1.deb"), "AAA")
	assertFile(t, filepath.Join(final, "pool/main/b/b/b_1.deb"), "BBB")
	// install tree present (boot source)
	assertFile(t, filepath.Join(final, "install.amd64/linux"), "KERNEL")
	// sentinel written
	if !dvdSentinelPresent(final) {
		t.Fatal("extraction sentinel must exist after a successful merge")
	}
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
