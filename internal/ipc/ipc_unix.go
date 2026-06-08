//go:build !windows

package ipc

import (
	"context"
	"net"
	"os"
)

func DefaultSocketPath() string {
	return "/var/run/flexconnect.sock"
}

func Listen(path string) (net.Listener, error) {
	_ = os.Remove(path)
	return net.Listen("unix", path)
}

func DialContext(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}
