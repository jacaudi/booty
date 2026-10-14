package tftp

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jeefy/booty/pkg/dataroot"
)

// openDataFile opens name from root if it is on the TFTP allowlist, both as
// requested and as resolved through any symlink (see [dataroot.Open]). Every
// refusal wraps [os.ErrNotExist] so the client sees "file not found" and learns
// nothing about why. [os.Root] refuses ".." and symlinks that leave root.
func openDataFile(root *os.Root, name string, topLevel []string) (*os.File, error) {
	if strings.Contains(name, "\x00") {
		return nil, fmt.Errorf("%w: NUL in name", os.ErrNotExist)
	}
	if strings.HasPrefix(name, "/") {
		return nil, fmt.Errorf("%w: absolute name", os.ErrNotExist)
	}
	f, err := dataroot.Open(root, name, func(p string) bool { return allowedDataFile(p, topLevel) })
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%w: %w", os.ErrNotExist, err), f.Close())
	}
	if fi.IsDir() {
		return nil, errors.Join(fmt.Errorf("%w: is a directory", os.ErrNotExist), f.Close())
	}
	return f, nil
}
