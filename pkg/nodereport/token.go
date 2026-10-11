package nodereport

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

const (
	keyLen  = 32
	keyMode = 0o600
)

// Token derives the per-host report token: lowercase hex of
// HMAC-SHA256(key, "booty-node-report\x00" + mac + "\x00" + gen).
func Token(key []byte, mac string, gen int64) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("booty-node-report\x00" + mac + "\x00" + strconv.FormatInt(gen, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// ValidToken reports whether presented is the token for mac and gen, in
// constant time.
func ValidToken(key []byte, mac string, gen int64, presented string) bool {
	return hmac.Equal([]byte(Token(key, mac, gen)), []byte(presented))
}

// LoadOrCreateKey returns the 32-byte token key at path, creating it with
// crypto/rand (atomically, mode 0600) when the file does not exist.
func LoadOrCreateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != keyLen {
			return nil, fmt.Errorf("node report key %s: %d bytes, want %d", path, len(key), keyLen)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read node report key: %w", err)
	}
	key = make([]byte, keyLen)
	if _, rerr := rand.Read(key); rerr != nil {
		return nil, fmt.Errorf("generate node report key: %w", rerr)
	}
	if werr := writeKey(path, key); werr != nil {
		return nil, werr
	}
	return key, nil
}

// writeKey writes key to a temp file beside path (mode 0600) and renames it
// into place, so a reader never sees a partial key.
func writeKey(path string, key []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".node-report-key-*")
	if err != nil {
		return fmt.Errorf("create node report key: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tmp.Close(), os.Remove(tmp.Name()))
		}
	}()
	if err = tmp.Chmod(keyMode); err != nil {
		return fmt.Errorf("chmod node report key: %w", err)
	}
	if _, err = tmp.Write(key); err != nil {
		return fmt.Errorf("write node report key: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close node report key: %w", err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("install node report key: %w", err)
	}
	return nil
}
