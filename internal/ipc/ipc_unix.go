//go:build !windows

package ipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func DefaultSocketPath() string {
	return "/var/run/flexconnect.sock"
}

func Listen(path string) (net.Listener, error) {
	// Unix sockets linger on disk after the listener exits. If a live listener
	// still exists on this path, keep it and report that the address is in use.
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return nil, fmt.Errorf("%s: address already in use", path)
	}
	_ = os.Remove(path)

	sockDir := filepath.Dir(path)
	if _, err := os.Stat(sockDir); os.IsNotExist(err) {
		_ = os.MkdirAll(sockDir, 0755)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0666); err != nil {
		ln.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return ln, nil
}

func DialContext(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}
