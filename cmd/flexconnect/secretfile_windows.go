//go:build windows

package main

import "os"

func validateSecretFile(os.FileInfo) error {
	return nil
}
