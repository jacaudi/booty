package config

import (
	"errors"
	"fmt"
	"os"

	"filippo.io/age"
	"github.com/spf13/viper"
)

// SecretsKey is the viper key for --secretsKey: the path to an age identity
// file (age-keygen output) that encrypts cluster secrets at rest (P6 §7).
const SecretsKey = "secretsKey"

// ErrNoSecretsKey is the fail-closed sentinel: --secretsKey is unset, so every
// cluster secrets operation (create/import/generate/export/serve-member) is
// refused. An encryption you can silently skip is not encryption (D2).
var ErrNoSecretsKey = errors.New("config: --secretsKey is not set; cluster secrets operations are disabled")

// LoadSecretsIdentity reads and parses the --secretsKey age identity file,
// returning the first X25519 identity it contains. Unset → ErrNoSecretsKey
// (fail-closed at the call site); set-but-unreadable/malformed → a descriptive
// error (which ValidateSecretsKey turns into a startup refusal, M2).
func LoadSecretsIdentity() (*age.X25519Identity, error) {
	path := viper.GetString(SecretsKey)
	if path == "" {
		return nil, ErrNoSecretsKey
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: open secretsKey %s: %w", path, err)
	}
	defer f.Close()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("config: parse secretsKey %s: %w", path, err)
	}
	for _, id := range ids {
		if x, ok := id.(*age.X25519Identity); ok {
			return x, nil
		}
	}
	return nil, fmt.Errorf("config: secretsKey %s contains no X25519 identity (want age-keygen output)", path)
}

// ValidateSecretsKey is the M2 startup gate, mirroring ValidateSignaturePolicy:
// an UNSET key is allowed (cluster ops fail closed per-operation instead), but
// a key that is set yet unreadable/malformed refuses startup — a broken key
// must never surface first as a mid-operation decrypt failure.
func ValidateSecretsKey() error {
	if viper.GetString(SecretsKey) == "" {
		return nil
	}
	if _, err := LoadSecretsIdentity(); err != nil {
		return fmt.Errorf("config: invalid secretsKey: %w", err)
	}
	return nil
}
