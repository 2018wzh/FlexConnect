//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSecretInputRejectsBroadFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSecretInput(path, false, strings.NewReader("")); err == nil {
		t.Fatal("world-readable password file succeeded")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	secret, provided, err := readSecretInput(path, false, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if !provided || secret != "secret" {
		t.Fatalf("secret=%q provided=%v", secret, provided)
	}
}
