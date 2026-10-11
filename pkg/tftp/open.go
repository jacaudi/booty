package tftp

import (
	"fmt"
	"os"
	"strings"
)

// openDataFile opens name from root if it is on the TFTP allowlist. Every
// refusal wraps [os.ErrNotExist] so the client sees "file not found" and learns
// nothing about why. [os.Root] refuses ".." and symlinks that leave root.
func openDataFile(root *os.Root, name string, topLevel []string) (*os.File, error) {
	if strings.Contains(name, "\x00") {
		return nil, fmt.Errorf("%w: NUL in name", os.ErrNotExist)
	}
	if strings.HasPrefix(name, "/") {
		return nil, fmt.Errorf("%w: absolute name", os.ErrNotExist)
	}
	if !allowedDataFile(name, topLevel) {
		return nil, fmt.Errorf("%w: not an allowlisted path", os.ErrNotExist)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", os.ErrNotExist, err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %w", os.ErrNotExist, err)
	}
	if fi.IsDir() {
		_ = f.Close()
		return nil, fmt.Errorf("%w: is a directory", os.ErrNotExist)
	}
	return f, nil
}
