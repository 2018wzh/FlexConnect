//go:build !windows && !linux

package ipc

func DefaultSocketPath() string {
	return "/var/run/flexconnect.sock"
}
