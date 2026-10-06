//go:build !windows

package security

import "os"

// replaceFile atomically moves src onto dst. On unix os.Rename is atomic and
// replaces the destination, so no extra flags are required.
func replaceFile(src, dst string) error {
	return os.Rename(src, dst)
}

// RestrictToOwner restricts path to the owner. The 0600 mode applied when the
// file is created is sufficient on unix; re-applying it is cheap and covers
// the case where the destination already existed with looser permissions.
func RestrictToOwner(path string) error {
	return os.Chmod(path, 0o600)
}
