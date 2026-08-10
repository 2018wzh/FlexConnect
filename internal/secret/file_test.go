package secret

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStorePutGetDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s := NewFileStore(path)

	if err := s.Put("profile/corp", "s3cret"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get("profile/corp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "s3cret" {
		t.Fatalf("Get = %q, want %q", got, "s3cret")
	}

	// Overwrite.
	if err := s.Put("profile/corp", "newpass"); err != nil {
		t.Fatalf("Put overwrite: %v", err)
	}
	got, err = s.Get("profile/corp")
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if got != "newpass" {
		t.Fatalf("Get = %q, want %q", got, "newpass")
	}

	if err := s.Delete("profile/corp"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("profile/corp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
}

func TestFileStorePersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := NewFileStore(path).Put("profile/a", "pw-a"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// A fresh instance reading the same path must see the stored secret.
	got, err := NewFileStore(path).Get("profile/a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "pw-a" {
		t.Fatalf("Get = %q, want %q", got, "pw-a")
	}
}

func TestFileStoreNotFoundOnMissingFile(t *testing.T) {
	s := NewFileStore(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if _, err := s.Get("profile/x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
	// Delete of a missing ref is idempotent and does not create the file.
	if err := s.Delete("profile/x"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(s.path); !os.IsNotExist(err) {
		t.Fatalf("secret file was created unexpectedly: %v", err)
	}
}

func TestFileStoreEmptyFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewFileStore(path)
	if _, err := s.Get("profile/x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
}

func TestFileStoreLeavesNoTemporaryFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secrets.json")
	if err := NewFileStore(path).Put("profile/corp", "pw"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("stale temp file left behind: %s", entry.Name())
		}
	}
}

func TestFileStoreDeletePersistsRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s := NewFileStore(path)
	if err := s.Put("profile/a", "pw-a"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Put("profile/b", "pw-b"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete("profile/a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	reloaded := NewFileStore(path)
	if _, err := reloaded.Get("profile/a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get deleted ref = %v, want ErrNotFound", err)
	}
	if got, err := reloaded.Get("profile/b"); err != nil || got != "pw-b" {
		t.Fatalf("Get remaining ref = %q, %v", got, err)
	}
}
