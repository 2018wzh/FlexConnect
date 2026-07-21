//go:build linux

package ipc

func DefaultSocketPath() string {
	return "/run/flexconnect/flexconnect.sock"
}
