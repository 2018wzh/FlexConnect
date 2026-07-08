package vpn

import (
	"context"
	"net"

	"flexconnect/internal/types"
)

type Event struct {
	Type    string
	Session *types.SessionInfo
	Err     error
}

type Backend interface {
	Connect(context.Context, types.Profile, string) (*types.SessionInfo, error)
	Disconnect(context.Context) error
	SessionInfo() *types.SessionInfo
	Traffic() *types.TrafficStats
	ReadServerConfig() map[string]any
	Events() <-chan Event
	TunnelDialer(context.Context) (TunnelDialer, error)
}

type TunnelDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
	LookupContextHost(context.Context, string) ([]string, error)
}
