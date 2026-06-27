package anyconnect

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"

	acAuth "flexconnect/internal/anyconnect/auth"
	acBase "flexconnect/internal/anyconnect/base"
	acRPC "flexconnect/internal/anyconnect/rpc"
	acSession "flexconnect/internal/anyconnect/session"
	"flexconnect/internal/types"
	"flexconnect/internal/vpn"
)

type Backend struct {
	mu     sync.Mutex
	events chan vpn.Event
}

func New() *Backend {
	acBase.Setup()
	acBase.Info("vpn backend initialized")
	return &Backend{events: make(chan vpn.Event, 32)}
}

func (b *Backend) Connect(ctx context.Context, profile types.Profile, password string) (*types.SessionInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	acBase.Info("vpn connect start", "host", profile.ServerURL, "username", profile.Username)
	configureProfile(profile, password)

	done := make(chan error, 1)
	go func() {
		done <- acRPC.Connect()
	}()

	select {
	case <-ctx.Done():
		acBase.Warn("vpn connect canceled", "error", ctx.Err().Error())
		acRPC.DisConnect()
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
			acBase.Warn("vpn connect failed", "error", err.Error())
			return nil, err
		}
		session := b.SessionInfo()
		acBase.Info("vpn connect success", "server", session.ServerAddress, "tun", session.TUNName)
		b.events <- vpn.Event{Type: "connected", Session: session}
		go b.monitorClose()
		return session, nil
	}
}

func (b *Backend) monitorClose() {
	if acSession.Sess.CloseChan == nil {
		acBase.Warn("monitor close skipped: close channel missing")
		return
	}
	acBase.Info("vpn monitor close started")
	<-acSession.Sess.CloseChan
	acBase.Info("vpn monitor close done")
	b.events <- vpn.Event{Type: "disconnected"}
}

func (b *Backend) Disconnect(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	acBase.Info("vpn disconnect called")
	acRPC.DisConnect()
	return nil
}

func (b *Backend) SessionInfo() *types.SessionInfo {
	c := acSession.Sess.CSess
	if c == nil {
		return nil
	}
	return &types.SessionInfo{
		ServerAddress: c.ServerAddress,
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
}

func (b *Backend) Traffic() *types.TrafficStats {
	c := acSession.Sess.CSess
	if c == nil || c.Stat == nil {
		return nil
	}
	return &types.TrafficStats{
		BytesSent:     c.Stat.BytesSent.Load(),
		BytesReceived: c.Stat.BytesReceived.Load(),
	}
}

func (b *Backend) ReadServerConfig() map[string]any {
	c := acSession.Sess.CSess
	if c == nil {
		return map[string]any{}
	}
	return map[string]any{
		"dns":           append([]string(nil), c.DNS...),
		"split_include": append([]string(nil), c.SplitInclude...),
		"split_exclude": append([]string(nil), c.SplitExclude...),
		"dynamic_split": c.DynamicSplitTunneling,
	}
}

func (b *Backend) Events() <-chan vpn.Event {
	return b.events
}

func configureProfile(profile types.Profile, password string) {
	host := profile.ServerURL
	if parsed, err := url.Parse(profile.ServerURL); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")

	acBase.Cfg.AgentName = "AnyConnect"
	acAuth.Prof.Host = host
	acAuth.Prof.GroupAccess = groupAccessURL(profile.ServerURL)
	acAuth.Prof.Username = profile.Username
	acAuth.Prof.Password = password
	acAuth.Prof.Group = profile.Group
	acAuth.Prof.SecretKey = ""
	acAuth.Prof.AcceptServerRoutes = profile.AcceptServerRoutes
	acAuth.Prof.CustomInclude = append([]string(nil), profile.CustomInclude...)
	acAuth.Prof.CustomExclude = append([]string(nil), profile.CustomExclude...)
	acAuth.Prof.DNSOverrides = append([]string(nil), profile.DNSOverrides...)
	acAuth.Prof.ApplyDNS = types.BoolValue(profile.ApplyDNS, true)
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
