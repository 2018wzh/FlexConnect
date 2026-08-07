//go:build !windows

package secret

import (
	"errors"
	"os"
	"syscall"
)

// syncDir flushes directory metadata so a rename survives a crash on Unix.
// Some filesystems (FUSE, network mounts) reject directory fsync with EINVAL;
// the file-level fsync in saveLocked already covers the data, so those errors
// are ignored to keep the fallback store usable everywhere.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
			return nil
		}
		return err
	}
	return nil
}
