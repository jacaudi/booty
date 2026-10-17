// Package keyfile loads, or creates once, the 32-byte random keys booty keeps
// in its data directory (the node-report key and the boot-ticket key).
package keyfile

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	keyLen  = 32
	keyMode = 0o600
)

// LoadOrCreate returns the 32-byte key at path, creating it with crypto/rand
// (atomically, mode 0600) when the file does not exist.
func LoadOrCreate(path string) ([]byte, error) {
	key, err := os.ReadFile(path) //nolint:gosec // G304: path is an operator-set dataDir key file, never request input
	if err == nil {
		if len(key) != keyLen {
			return nil, fmt.Errorf("key %s: %d bytes, want %d", path, len(key), keyLen)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read key %s: %w", path, err)
	}
	key = make([]byte, keyLen)
	if _, rerr := rand.Read(key); rerr != nil {
		return nil, fmt.Errorf("generate key %s: %w", path, rerr)
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
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create key %s: %w", path, err)
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
		return errors.Join(fmt.Errorf("chmod key %s: %w", path, cerr), tmp.Close())
	}
	if _, werr := tmp.Write(key); werr != nil {
		return errors.Join(fmt.Errorf("write key %s: %w", path, werr), tmp.Close())
	}
	if serr := tmp.Sync(); serr != nil {
		return errors.Join(fmt.Errorf("sync key %s: %w", path, serr), tmp.Close())
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("close key %s: %w", path, cerr)
	}
	if rerr := os.Rename(tmpPath, path); rerr != nil {
		return fmt.Errorf("install key %s: %w", path, rerr)
	}
	return syncDir(dir)
}

// syncDir fsyncs a directory so a rename into it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: dir is the operator-set dataDir, never request input
	if err != nil {
		return fmt.Errorf("open key dir %s: %w", dir, err)
	}
	if serr := d.Sync(); serr != nil {
		return errors.Join(fmt.Errorf("sync key dir %s: %w", dir, serr), d.Close())
	}
	if cerr := d.Close(); cerr != nil {
		return fmt.Errorf("close key dir %s: %w", dir, cerr)
	}
	return nil
}
