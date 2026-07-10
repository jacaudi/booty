package http

import (
	"bytes"
	"fmt"
	"io"

	"filippo.io/age"
	"github.com/jeefy/booty/pkg/config"
)

// encryptSecrets age-encrypts plaintext under the operator's --secretsKey
// identity (its own recipient — one key encrypts and decrypts). Fail-closed:
// with no key configured the error wraps config.ErrNoSecretsKey and the caller
// refuses the operation (P6 §7).
func encryptSecrets(plain []byte) ([]byte, error) {
	id, err := config.LoadSecretsIdentity()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, id.Recipient())
	if err != nil {
		return nil, fmt.Errorf("http: age encrypt: %w", err)
	}
	if _, err := w.Write(plain); err != nil {
		return nil, fmt.Errorf("http: age write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("http: age finalize: %w", err)
	}
	return buf.Bytes(), nil
}

// decryptSecrets reverses encryptSecrets. A wrong key or corrupt ciphertext is
// a loud error — a broken secret must never be served or acted on.
func decryptSecrets(enc []byte) ([]byte, error) {
	id, err := config.LoadSecretsIdentity()
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(bytes.NewReader(enc), id)
	if err != nil {
		return nil, fmt.Errorf("http: age decrypt: %w", err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("http: age read: %w", err)
	}
	return plain, nil
}
