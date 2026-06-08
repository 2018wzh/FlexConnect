//go:build !windows

package main

func ensureElevated() error {
	return nil
}
