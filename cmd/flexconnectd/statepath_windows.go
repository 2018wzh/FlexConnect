//go:build windows

package main

import (
	"os"
	"path/filepath"
)

func defaultStatePath() string {
	if programData := os.Getenv("ProgramData"); programData != "" {
		return filepath.Join(programData, "FlexConnect", "state.json")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "flexconnect-state.json"
	}
	return filepath.Join(dir, "FlexConnect", "state.json")
}
