package nodereport

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
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
//
// It returns "" when key is not keyLen bytes, which ValidToken rejects.
func Token(key []byte, mac string, gen int64) string {
	if len(key) != keyLen {
		return ""
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("booty-node-report\x00" + mac + "\x00" + strconv.FormatInt(gen, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// ValidToken reports whether presented is the token for mac and gen, in
// constant time.
func ValidToken(key []byte, mac string, gen int64, presented string) bool {
	want := Token(key, mac, gen)
	if want == "" {
		return false
	}
	return hmac.Equal([]byte(want), []byte(presented))
}

// LoadOrCreateKey returns the 32-byte token key at path, creating it with
// crypto/rand (atomically, mode 0600) when the file does not exist.
func LoadOrCreateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path) //nolint:gosec // G304: path is the operator-set dataDir key file, never request input
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

// writeKey writes key to a temp file beside path (mode 0600), syncs it,
// renames it into place and syncs the directory, so a reader never sees a
// partial key and a crash cannot lose a key that was handed out.
func writeKey(path string, key []byte) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".node-report-key-*")
	if err != nil {
		return fmt.Errorf("create node report key: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		// A successful rename leaves nothing at tmpPath; on error discard it.
		if err == nil {
			return
		}
		if rmErr := os.Remove(tmpPath); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = errors.Join(err, rmErr)
		}
	}()
	if cerr := tmp.Chmod(keyMode); cerr != nil {
		return errors.Join(fmt.Errorf("chmod node report key: %w", cerr), tmp.Close())
	}
	if _, werr := tmp.Write(key); werr != nil {
		return errors.Join(fmt.Errorf("write node report key: %w", werr), tmp.Close())
	}
	if serr := tmp.Sync(); serr != nil {
		return errors.Join(fmt.Errorf("sync node report key: %w", serr), tmp.Close())
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("close node report key: %w", cerr)
	}
	if rerr := os.Rename(tmpPath, path); rerr != nil {
		return fmt.Errorf("install node report key: %w", rerr)
	}
	return syncDir(dir)
}

// syncDir fsyncs a directory so a rename into it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: dir is the operator-set dataDir, never request input
	if err != nil {
		return fmt.Errorf("open node report key dir: %w", err)
	}
	if serr := d.Sync(); serr != nil {
		return errors.Join(fmt.Errorf("sync node report key dir: %w", serr), d.Close())
	}
	if cerr := d.Close(); cerr != nil {
		return fmt.Errorf("close node report key dir: %w", cerr)
	}
	return nil
}
