//go:build !windows

package main

import "context"

func isWindowsService() bool {
	return false
}

func runWindowsService(opts daemonOptions) error {
	return runDaemon(context.Background(), opts)
}
