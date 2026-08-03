package rpc

import (
	"context"
	"fmt"
	"strings"

	"flexconnect/internal/anyconnect/auth"
	"flexconnect/internal/anyconnect/base"
	"flexconnect/internal/anyconnect/session"
	acvpn "flexconnect/internal/anyconnect/tunnel"
	"flexconnect/internal/osnet"
)

// Connection owns the authentication and VPN session state for one connect
// attempt.
type Connection struct {
	Auth    *auth.Client
	Session *session.Session
}

func NewConnection(profile auth.Profile) *Connection {
	return &Connection{Auth: auth.NewClient(profile, base.Interface{}), Session: &session.Session{}}
}

func (c *Connection) Connect(ctx context.Context) error {
	if c == nil || c.Auth == nil || c.Session == nil {
		return fmt.Errorf("invalid VPN connection")
	}
	if ctx == nil {
		return fmt.Errorf("nil VPN connection context")
	}
	if err := refreshConnectionInterface(c); err != nil {
		return err
	}
	if strings.Contains(c.Auth.Prof.Host, ":") {
		c.Auth.Prof.HostWithPort = c.Auth.Prof.Host
	} else {
		c.Auth.Prof.HostWithPort = c.Auth.Prof.Host + ":443"
	}
	if err := c.Auth.InitAuth(nil); err != nil {
		return err
	}
	if err := c.Auth.PasswordAuth(c.Session); err != nil {
		_ = c.Auth.Close()
		return err
	}
	if err := acvpn.SetupTunnelWithClient(c.Auth, c.Session); err != nil {
		_ = c.Auth.Close()
		return err
	}
	return nil
}

func (c *Connection) Disconnect(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.Session.ActiveClose = true
	var first error
	if c.Auth != nil {
		if err := c.Auth.Close(); err != nil {
			first = err
		}
	}
	if c.Session.CSess != nil {
		if c.Session.CSess.NetworkManager != nil {
			if err := c.Session.CSess.NetworkManager.Close(ctx); err != nil && first == nil {
				first = err
			}
		}
		c.Session.CSess.RecordClose("local_requested", "local", nil)
		c.Session.CSess.Close()
	}
	return first
}

func refreshConnectionInterface(c *Connection) error {
	info, err := osnet.GetLocalInterface(context.Background())
	if err != nil {
		return err
	}
	c.Auth.LocalInterface = base.Interface{Name: info.Name, Ip4: info.IP4, Mac: info.MAC, Gateway: info.Gateway}
	return nil
}
