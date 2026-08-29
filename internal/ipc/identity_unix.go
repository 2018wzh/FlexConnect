//go:build !windows

package ipc

import "net"

func platformConnIdentity(net.Conn) (Identity, error) {
	// Unix socket ownership and group mode remain the authorization boundary on
	// Unix in 1.3.0. Windows uses token-derived per-user ownership.
	return Identity{ID: "system", System: true, Elevated: true}, nil
}
