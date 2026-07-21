//go:build !windows

package main

import (
	"fmt"
	"os"
)

func validateSecretFile(info os.FileInfo) error {
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("password file permissions %04o are too broad; require 0600 or stricter", mode)
	}
	return nil
}
