package anyconnect

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	acAuth "flexconnect/internal/anyconnect/auth"
	acBase "flexconnect/internal/anyconnect/base"
	acRPC "flexconnect/internal/anyconnect/rpc"
	acSession "flexconnect/internal/anyconnect/session"
	acTunnel "flexconnect/internal/anyconnect/tunnel"
	"flexconnect/internal/osnet"
	"flexconnect/internal/types"
	"flexconnect/internal/vpn"
)

type Backend struct {
	mu            sync.Mutex
	events        chan vpn.Event
	connectionSeq uint64
	monitorMu     sync.Mutex
	monitor       osnet.Monitor
	monitorCancel context.CancelFunc
	monitorID     string
	active        *acRPC.Connection
}

func New() *Backend {
	acBase.Setup()
	acBase.Info("vpn backend initialized")
	return &Backend{events: make(chan vpn.Event, 32)}
}

func (b *Backend) Connect(ctx context.Context, profile types.Profile, password string) (*types.SessionInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// A lingering connection means the previous teardown never completed; a
	// new session would reuse the same TUN device name and fight the stale
	// packet workers for routes. Drain it before dialing.
	if stale := b.active; stale != nil {
		acBase.Warn("tearing down lingering connection before new connect")
		_ = stale.Disconnect(context.Background())
		b.active = nil
	}

	acBase.Info("vpn connect start", "host", profile.ServerURL, "username", profile.Username)
	connection := acRPC.NewConnection(buildAuthProfile(profile, password))
	b.active = connection

	done := make(chan error, 1)
	go func() {
		done <- connection.Connect(ctx)
	}()

	select {
	case <-ctx.Done():
		acBase.Warn("vpn connect canceled", "error", ctx.Err().Error())
		_ = connection.Disconnect(context.Background())
		select {
		case err := <-done:
			if err != nil {
				acBase.Warn("vpn connect cleanup completed", "error", err.Error())
			} else {
				acBase.Info("vpn connect cleanup completed")
			}
		case <-time.After(30 * time.Second):
			acBase.Warn("vpn connect cleanup timed out")
		}
		return nil, ctx.Err()
	case err := <-done:
		if err != nil {
			_ = connection.Disconnect(context.Background())
			b.active = nil
			acBase.Warn("vpn connect failed", "error", err.Error())
			return nil, err
		}
		cSess := connection.Session.CSess
		if cSess == nil {
			b.active = nil
			return nil, fmt.Errorf("vpn connect completed without a connection session")
		}
		b.connectionSeq++
		cSess.ConnectionID = fmt.Sprintf("vpn-%d", b.connectionSeq)
		session := b.sessionInfo(cSess)
		acBase.Info("vpn connect success", "server", session.ServerAddress, "tun", session.TUNName)
		b.events <- vpn.Event{Type: "connected", ConnectionID: cSess.ConnectionID, Session: session}
		if err := b.startUnderlayMonitor(cSess, cSess.ConnectionID); err != nil {
			acBase.Error("underlay monitor start failed:", err)
			_ = connection.Disconnect(context.Background())
			b.active = nil
			return nil, fmt.Errorf("start underlay monitor: %w", err)
		}
		go b.monitorClose(cSess, cSess.ConnectionID)
		return session, nil
	}
}

func (b *Backend) monitorClose(cSess *acSession.ConnSession, connectionID string) {
	if cSess == nil || cSess.CloseChan == nil {
		acBase.Warn("monitor close skipped: close channel missing")
		return
	}
	acBase.Info("vpn monitor close started")
	<-cSess.CloseChan
	acBase.Info("vpn monitor close done")
	info := cSess.CloseInfo()
	faults := make([]vpn.TransportFault, 0, len(info.TransportFaults))
	for _, fault := range info.TransportFaults {
		faults = append(faults, vpn.TransportFault{Code: fault.Code, Transport: fault.Transport, Error: fault.Error, Time: fault.Time})
	}
	b.events <- vpn.Event{
		Type: "disconnected", ConnectionID: connectionID,
		Close: &vpn.DisconnectInfo{Code: info.Code, Transport: info.Transport, Error: info.Error, Time: info.Time, TransportFaults: faults},
	}
}

func (b *Backend) Disconnect(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	acBase.Info("vpn disconnect called")
	b.stopUnderlayMonitor()
	connection := b.active
	b.active = nil
	if connection != nil {
		return connection.Disconnect(ctx)
	}
	return nil
}

func (b *Backend) SessionInfo() *types.SessionInfo {
	c := b.activeSession()
	if c == nil {
		return nil
	}
	return b.sessionInfo(c)
}

func (b *Backend) sessionInfo(c *acSession.ConnSession) *types.SessionInfo {
	if c == nil {
		return nil
	}
	info := &types.SessionInfo{
		ConnectionID:  c.ConnectionID,
		ServerAddress: c.ServerAddress,
		LocalAddress:  c.LocalSocketAddress,
		RemoteAddress: c.RemoteSocketAddress,
		Hostname:      c.Hostname,
		TUNName:       c.TunName,
		VPNAddress:    c.VPNAddress,
		VPNMask:       c.VPNMask,
		DNS:           append([]string(nil), c.DNS...),
		MTU:           c.MTU,
		SplitInclude:  append([]string(nil), c.SplitInclude...),
		SplitExclude:  append([]string(nil), c.SplitExclude...),
		TLSCipher:     c.TLSCipherSuite,
		DTLSCipher:    c.DTLSCipherSuite,
	}
	if c.Underlay.LocalIPv4.IsValid() {
		info.Underlay = &types.UnderlayInfo{
			InterfaceName: c.Underlay.InterfaceName, InterfaceIndex: c.Underlay.InterfaceIndex,
			LocalIPv4: c.Underlay.LocalIPv4.String(), Gateway: c.Underlay.Gateway.String(),
			GatewayInterface: c.Underlay.GatewayInterface, RouteMetric: c.Underlay.RouteMetric,
			Generation: c.Underlay.Generation,
		}
	}
	return info
}

func (b *Backend) Traffic() *types.TrafficStats {
	c := b.activeSession()
	if c == nil || c.Stat == nil {
		return nil
	}
	return &types.TrafficStats{
		BytesSent:     c.Stat.BytesSent.Load(),
		BytesReceived: c.Stat.BytesReceived.Load(),
	}
}

func (b *Backend) ReadServerConfig() map[string]any {
	c := b.activeSession()
	if c == nil {
		return map[string]any{}
	}
	return map[string]any{
		"dns":           append([]string(nil), c.DNS...),
		"split_include": append([]string(nil), c.SplitInclude...),
		"split_exclude": append([]string(nil), c.SplitExclude...),
		"dynamic_split": c.DynamicSplitTunneling,
		"underlay":      b.sessionInfo(c).Underlay,
		"runtime":       b.runtimeDiagnostics(c),
	}
}

func (b *Backend) RuntimeDiagnostics() *types.RuntimeDiagnostics {
	c := b.activeSession()
	if c == nil {
		return nil
	}
	return b.runtimeDiagnostics(c)
}

func (b *Backend) runtimeDiagnostics(c *acSession.ConnSession) *types.RuntimeDiagnostics {
	if c == nil {
		return nil
	}
	stats := c.RuntimeStats()
	runtime := &types.TunnelRuntime{
		State: c.LifecycleState.Load(), TLSState: c.TLSState.Load(), DTLSState: c.DTLSState.Load(),
		TUNReads: stats.TUNReads, TUNWrites: stats.TUNWrites,
		TUNReadErrors: stats.TUNReadErrors, TUNWriteErrors: stats.TUNWriteErrors,
		DPDSent: stats.DPDSent, DPDResponses: stats.DPDResponses, QueueDrops: stats.QueueDrops,
	}
	var underlay *types.UnderlayInfo
	if info := b.sessionInfo(c); info != nil {
		underlay = info.Underlay
	}
	return &types.RuntimeDiagnostics{Underlay: underlay, Tunnel: runtime}
}

func (b *Backend) startUnderlayMonitor(cSess *acSession.ConnSession, connectionID string) error {
	b.monitorMu.Lock()
	defer b.monitorMu.Unlock()
	if b.monitor != nil {
		return errors.New("underlay monitor already active")
	}
	ctx, cancel := context.WithCancel(context.Background())
	monitor, err := osnet.NewMonitor(ctx, osnet.MonitorOptions{ExcludeInterface: cSess.TunName})
	if err != nil {
		cancel()
		return err
	}
	if status, ok := monitor.(interface{ NotifierError() error }); ok {
		if notifierErr := status.NotifierError(); notifierErr != nil {
			acBase.Warn("underlay notifications unavailable; polling fallback active:", notifierErr)
		}
	}
	b.monitor = monitor
	b.monitorCancel = cancel
	b.monitorID = connectionID
	go b.watchUnderlay(ctx, monitor, connectionID)
	return nil
}

func (b *Backend) watchUnderlay(ctx context.Context, monitor osnet.Monitor, connectionID string) {
	changes := monitor.Changes(ctx)
	for change := range changes {
		event := vpn.Event{Type: "network_change", ConnectionID: connectionID, Network: convertNetworkChange(change)}
		select {
		case b.events <- event:
		case <-ctx.Done():
			return
		}
		// One event starts one controlled repair transaction. The next monitor
		// instance is created after the new connection is established.
		b.stopUnderlayMonitor()
		return
	}
}

func (b *Backend) stopUnderlayMonitor() {
	b.monitorMu.Lock()
	defer b.monitorMu.Unlock()
	if b.monitorCancel != nil {
		b.monitorCancel()
	}
	if b.monitor != nil {
		_ = b.monitor.Close()
	}
	b.monitor = nil
	b.monitorCancel = nil
	b.monitorID = ""
}

func convertNetworkChange(change osnet.UnderlayChange) *vpn.NetworkChange {
	return &vpn.NetworkChange{
		Before: convertNetworkSnapshot(change.Before), After: convertNetworkSnapshot(change.After),
		Reasons: append([]string(nil), change.Reasons...), RebindRequired: change.RebindRequired,
		Error: diagnosticNetworkError(change.Err),
	}
}

func convertNetworkSnapshot(snapshot osnet.UnderlaySnapshot) vpn.NetworkSnapshot {
	return vpn.NetworkSnapshot{
		InterfaceName: snapshot.InterfaceName, InterfaceIndex: snapshot.InterfaceIndex,
		LocalIPv4: snapshot.LocalIPv4.String(), Gateway: snapshot.Gateway.String(),
		GatewayInterface: snapshot.GatewayInterface, RouteMetric: snapshot.RouteMetric,
		Generation: snapshot.Generation,
	}
}

func diagnosticNetworkError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func (b *Backend) Events() <-chan vpn.Event {
	return b.events
}

func (b *Backend) TunnelDialer(ctx context.Context) (vpn.TunnelDialer, error) {
	return acTunnel.SessionTunnelDialer(ctx, b.activeSession())
}

func (b *Backend) activeSession() *acSession.ConnSession {
	if b == nil || b.active == nil || b.active.Session == nil {
		return nil
	}
	return b.active.Session.CSess
}

func buildAuthProfile(profile types.Profile, password string) acAuth.Profile {
	host := profile.ServerURL
	if parsed, err := url.Parse(profile.ServerURL); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")

	acBase.Cfg.AgentName = "AnyConnect"
	return acAuth.Profile{
		Host: host, GroupAccess: groupAccessURL(profile.ServerURL), Username: profile.Username,
		Password: password, Group: profile.Group, SecretKey: "", Scheme: "https://",
		AcceptServerRoutes: profile.AcceptServerRoutes, ApplyDNS: types.BoolValue(profile.ApplyDNS, true),
		CustomInclude: append([]string(nil), profile.CustomInclude...),
		CustomExclude: append([]string(nil), profile.CustomExclude...),
		DNSOverrides:  append([]string(nil), profile.DNSOverrides...), MTU: profile.MTU,
	}
}

func groupAccessURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		host := strings.TrimPrefix(rawURL, "https://")
		host = strings.TrimPrefix(host, "http://")
		return "https://" + strings.TrimRight(host, "/")
	}
	parsed.Scheme = "https"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}
