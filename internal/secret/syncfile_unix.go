//go:build !windows

package secret

import "os"

func replaceAtomic(source, destination string) error { return os.Rename(source, destination) }

// syncDir flushes directory metadata so a rename survives a crash on Unix.
// Directory fsync errors are returned: the explicit plaintext store must not
// claim a durable commit when the directory entry could still be lost.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func secureFile(path string) error {
	return os.Chmod(path, 0o600)
}

func secureDir(path string) error { return os.Chmod(path, 0o700) }
