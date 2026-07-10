package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/spf13/viper"
)

func TestLoadSecretsIdentityUnsetIsFailClosedSentinel(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	if _, err := LoadSecretsIdentity(); !errors.Is(err, ErrNoSecretsKey) {
		t.Fatalf("unset --secretsKey = %v, want ErrNoSecretsKey", err)
	}
	// Unset is NOT a startup error (fail-closed, not fail-fast — M2).
	if err := ValidateSecretsKey(); err != nil {
		t.Fatalf("ValidateSecretsKey(unset) = %v, want nil", err)
	}
}

func TestLoadSecretsIdentityParsesAgeKeygenFile(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key.txt")
	// age-keygen writes comment lines + the AGE-SECRET-KEY line; mirror that.
	content := "# created: 2026-07-09\n# public key: " + id.Recipient().String() + "\n" + id.String() + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set(SecretsKey, path)

	got, err := LoadSecretsIdentity()
	if err != nil {
		t.Fatalf("LoadSecretsIdentity: %v", err)
	}
	if got.Recipient().String() != id.Recipient().String() {
		t.Fatal("loaded identity does not match the file's key")
	}
	if err := ValidateSecretsKey(); err != nil {
		t.Fatalf("ValidateSecretsKey(valid) = %v", err)
	}
}

func TestValidateSecretsKeyFailsFastOnBrokenKey(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	// Set-but-missing file → startup must refuse (M2).
	viper.Set(SecretsKey, filepath.Join(t.TempDir(), "nope.txt"))
	if err := ValidateSecretsKey(); err == nil {
		t.Fatal("missing key file accepted at startup")
	}

	// Set-but-malformed → startup must refuse.
	bad := filepath.Join(t.TempDir(), "garbage.txt")
	if err := os.WriteFile(bad, []byte("not an age identity\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set(SecretsKey, bad)
	if err := ValidateSecretsKey(); err == nil {
		t.Fatal("malformed key file accepted at startup")
	}
}
