//go:build !windows

package osnet

import "os"

func replaceJournalFile(source, target string) error { return os.Rename(source, target) }

func secureJournalFile(path string) error { return os.Chmod(path, 0o600) }

func syncJournalDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
