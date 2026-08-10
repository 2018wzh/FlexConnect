//go:build !windows

package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreFilePermissions(t *testing.T) {
	// The nested directory is created by the store itself; t.TempDir()'s own
	// mode varies by environment, so only assert what the store controls.
	path := filepath.Join(t.TempDir(), "nested", "secrets.json")
	if err := NewFileStore(path).Put("profile/corp", "pw"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("secret file mode = %o, want 600", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("secret dir mode = %o, want 700", perm)
	}
}
