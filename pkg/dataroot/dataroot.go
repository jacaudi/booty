// Package dataroot opens files from booty's data directory on behalf of the two
// servers that publish it, TFTP (pkg/tftp) and HTTP /data/ (pkg/http), so the
// rule they share lives in one place: a name is served only if it is
// allowlisted both as requested AND as resolved through every symlink.
//
// [os.Root] alone is not enough. It refuses symlinks that leave the root, but
// it follows a relative one that stays inside, so "public/x -> ../api-token"
// would pass an allowlist applied to the requested name and serve a file the
// allowlist never admitted.
package dataroot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
)

// maxLinkHops bounds symlink expansion while resolving one name. 40 is Linux's
// own limit (MAXSYMLINKS), so a loop fails here as it would in the kernel.
const maxLinkHops = 40

var (
	errEscapesRoot  = errors.New("path leaves the root")
	errAbsoluteLink = errors.New("absolute symlink")
	errTooManyLinks = errors.New("too many symlinks")
)

// Open opens name from root if allowed accepts it twice: as requested, and as
// resolved through every symlink along the way. Every refusal and open failure
// wraps [os.ErrNotExist], so a client learns nothing about why a name was
// refused. The caller owns rejecting what Open does not: a directory result.
//
// allowed receives a slash-separated path relative to root, with no leading
// slash. The requested name is judged first, so a non-allowlisted request never
// costs a filesystem walk.
//
// The file is opened by its resolved, symlink-free path, and still through
// root, so a link swapped in after the check can at worst be followed inside
// root. Swapping one needs write access to the data directory, which is the
// operator's; Open does not claim to defend against that.
func Open(root *os.Root, name string, allowed func(string) bool) (*os.File, error) {
	if !allowed(name) {
		return nil, fmt.Errorf("%w: not an allowlisted path", os.ErrNotExist)
	}
	resolved, err := resolve(root, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", os.ErrNotExist, err)
	}
	if !allowed(resolved) {
		return nil, fmt.Errorf("%w: resolves to a non-allowlisted path", os.ErrNotExist)
	}
	f, err := root.Open(resolved)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", os.ErrNotExist, err)
	}
	return f, nil
}

// resolve returns name with every symlink expanded, as a slash-separated path
// relative to root. It walks one component at a time, so ".." pops the
// already-resolved prefix (the kernel's semantics, not a lexical clean), and a
// symlink in any intermediate directory is expanded too.
func resolve(root *os.Root, name string) (string, error) {
	var resolved string // symlink-free prefix; "" is root itself
	pending := strings.Split(name, "/")
	hops := 0
	for len(pending) > 0 {
		comp := pending[0]
		pending = pending[1:]
		switch comp {
		case "", ".":
			continue
		case "..":
			if resolved == "" {
				return "", errEscapesRoot
			}
			if resolved = path.Dir(resolved); resolved == "." {
				resolved = ""
			}
			continue
		}
		cur := path.Join(resolved, comp)
		fi, err := root.Lstat(cur)
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			resolved = cur
			continue
		}
		hops++
		if hops > maxLinkHops {
			return "", errTooManyLinks
		}
		target, err := root.Readlink(cur)
		if err != nil {
			return "", err
		}
		if path.IsAbs(target) {
			return "", errAbsoluteLink
		}
		// The target is relative to the directory holding the link, which is
		// already resolved; expand it in place ahead of what remains.
		pending = append(strings.Split(target, "/"), pending...)
	}
	return resolved, nil
}
