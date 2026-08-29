//go:build !windows

package file

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}

func syncParentDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func secureFile(path string) error {
	return os.Chmod(path, 0o600)
}
