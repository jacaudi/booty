package http

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

// testSecretsKey mints a fresh age identity file and points --secretsKey at
// it. Call AFTER any fixture that resets viper (servingStore et al.). Shared
// by the serving-rung and clusters-API suites.
func testSecretsKey(t *testing.T) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secrets-key.txt")
	if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set(config.SecretsKey, path)
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	testSecretsKey(t)

	plain := []byte("version: v1alpha1\nmachine:\n  type: controlplane\n")
	enc, err := encryptSecrets(plain)
	if err != nil {
		t.Fatalf("encryptSecrets: %v", err)
	}
	if string(enc) == string(plain) {
		t.Fatal("ciphertext equals plaintext")
	}
	got, err := decryptSecrets(enc)
	if err != nil {
		t.Fatalf("decryptSecrets: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("round-trip = %q, want %q", got, plain)
	}
}

func TestEncryptFailClosedWithoutKey(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	if _, err := encryptSecrets([]byte("x")); !errors.Is(err, config.ErrNoSecretsKey) {
		t.Fatalf("encrypt without key = %v, want ErrNoSecretsKey", err)
	}
	if _, err := decryptSecrets([]byte("x")); !errors.Is(err, config.ErrNoSecretsKey) {
		t.Fatalf("decrypt without key = %v, want ErrNoSecretsKey", err)
	}
}

func TestDecryptWithWrongKeyFailsLoudly(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	testSecretsKey(t)
	enc, err := encryptSecrets([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	// Swap in a DIFFERENT identity: decrypt must error, never return garbage.
	testSecretsKey(t)
	if _, err := decryptSecrets(enc); err == nil {
		t.Fatal("decrypt with the wrong key succeeded")
	}
}
