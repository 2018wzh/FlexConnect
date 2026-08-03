package vpn

import (
	"context"
	"net"

	"flexconnect/internal/types"
)

type Event struct {
	Type         string
	ConnectionID string
	Session      *types.SessionInfo
	Err          error
	Close        *DisconnectInfo
	Network      *NetworkChange
}

type NetworkSnapshot struct {
	InterfaceName    string
	InterfaceIndex   int
	LocalIPv4        string
	Gateway          string
	GatewayInterface int
	RouteMetric      int
	Generation       uint64
}

type NetworkChange struct {
	Before         NetworkSnapshot
	After          NetworkSnapshot
	Reasons        []string
	RebindRequired bool
	Error          string
}

type DisconnectInfo struct {
	Code            string
	Transport       string
	Error           string
	Time            string
	TransportFaults []TransportFault
}

type TransportFault struct {
	Code      string
	Transport string
	Error     string
	Time      string
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
