//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

func defaultStatePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "flexconnect-state.json"
	}
	return filepath.Join(dir, "FlexConnect", "state.json")
}
