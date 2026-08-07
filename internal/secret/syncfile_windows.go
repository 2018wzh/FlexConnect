//go:build windows

package secret

// syncDir is a no-op on Windows: NTFS metadata updates are journaled and
// directories cannot be opened for syncing like on Unix.
func syncDir(dir string) error {
	return nil
}
