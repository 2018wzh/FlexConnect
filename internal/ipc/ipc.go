package ipc

import (
	"context"
	"net"
)

const LocalAPIHost = "local-flexconnectd.sock"

type DialFunc func(context.Context, string) (net.Conn, error)

type Identity struct {
	ID       string
	System   bool
	Elevated bool
}

type identityConn struct {
	net.Conn
	identity Identity
}

func (c *identityConn) Identity() Identity { return c.identity }

func IdentityFromConn(conn net.Conn) (Identity, bool) {
	provider, ok := conn.(interface{ Identity() Identity })
	if !ok {
		return Identity{}, false
	}
	return provider.Identity(), true
}

type identityListener struct{ net.Listener }

func (l identityListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	identity, err := platformConnIdentity(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &identityConn{Conn: conn, identity: identity}, nil
}
