//go:build darwin

package osnet

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"sync"

	wgtun "github.com/tailscale/wireguard-go/tun"
)

type platformManager struct {
	mu             sync.Mutex
	name           string
	gateway        netip.Addr
	vpnAddr        netip.Addr
	serverRoutes   map[netip.Prefix]bool
	includeRoutes  map[netip.Prefix]bool
	excludeRoutes  map[netip.Prefix]bool
	dynamicInclude map[netip.Prefix]bool
	dynamicExclude map[netip.Prefix]bool
	dnsSet         bool
}

func newPlatformManager(_ wgtun.Device, name string) (Manager, error) {
	return &platformManager{
		name:           name,
		serverRoutes:   map[netip.Prefix]bool{},
		includeRoutes:  map[netip.Prefix]bool{},
		excludeRoutes:  map[netip.Prefix]bool{},
		dynamicInclude: map[netip.Prefix]bool{},
		dynamicExclude: map[netip.Prefix]bool{},
	}, nil
}

func (m *platformManager) Up(context.Context) error { return nil }

func (m *platformManager) Set(ctx context.Context, cfg *Config) error {
	if cfg == nil || !cfg.VPNAddress.IsValid() {
		return m.Close(ctx)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vpnAddr = cfg.VPNAddress.Addr()
	m.gateway = cfg.Gateway
	if cfg.ServerAddress.IsValid() && !cfg.Gateway.IsValid() {
		return fmt.Errorf("missing physical underlay gateway for VPN server route")
	}
	if err := run(ctx, "ifconfig", m.name, "inet", m.vpnAddr.String(), m.vpnAddr.String(), "netmask", "255.255.255.255", "up"); err != nil {
		return err
	}
	var server []netip.Prefix
	if cfg.ServerAddress.IsValid() && cfg.Gateway.IsValid() {
		server = append(server, netip.PrefixFrom(cfg.ServerAddress, cfg.ServerAddress.BitLen()))
	}
	if err := m.syncRoutes(ctx, &m.serverRoutes, server, m.gateway); err != nil {
		return err
	}
	if err := m.syncRoutes(ctx, &m.includeRoutes, cfg.IncludeRoutes, m.vpnAddr); err != nil {
		return err
	}
	if err := m.syncRoutes(ctx, &m.excludeRoutes, withoutPrefixes(cfg.ExcludeRoutes, server), m.gateway); err != nil {
		return err
	}
	if len(cfg.DNSServers) > 0 {
		if err := setDarwinDNS(ctx, cfg.DNSServers); err != nil {
			return err
		}
		m.dnsSet = true
	} else if m.dnsSet {
		if err := clearDarwinDNS(ctx); err != nil {
			return err
		}
		m.dnsSet = false
	}
	return nil
}

func (m *platformManager) SetDynamicRoutes(ctx context.Context, routes DynamicRoutes) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.syncRoutes(ctx, &m.dynamicInclude, addrsToHostPrefixes(routes.Include), m.vpnAddr); err != nil {
		return err
	}
	return m.syncRoutes(ctx, &m.dynamicExclude, addrsToHostPrefixes(routes.Exclude), m.gateway)
}

func (m *platformManager) Close(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	groups := []struct {
		routes  *map[netip.Prefix]bool
		gateway netip.Addr
	}{
		{&m.serverRoutes, m.gateway}, {&m.includeRoutes, m.vpnAddr}, {&m.excludeRoutes, m.gateway},
		{&m.dynamicInclude, m.vpnAddr}, {&m.dynamicExclude, m.gateway},
	}
	for _, group := range groups {
		for prefix := range *group.routes {
			if err := deleteDarwinOwnedRoute(ctx, prefix.String(), group.gateway.String()); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		*group.routes = map[netip.Prefix]bool{}
	}
	if m.dnsSet {
		if err := clearDarwinDNS(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
		m.dnsSet = false
	}
	return firstErr
}

func (m *platformManager) syncRoutes(ctx context.Context, old *map[netip.Prefix]bool, next []netip.Prefix, gateway netip.Addr) error {
	if !gateway.IsValid() {
		return nil
	}
	add, del, state := DiffPrefixes(*old, next)
	owned := make(map[netip.Prefix]bool, len(*old))
	for prefix := range *old {
		owned[prefix] = true
	}
	for _, prefix := range add {
		if err := run(ctx, "route", "add", "-net", prefix.String(), gateway.String()); err != nil {
			*old = owned
			return err
		}
		owned[prefix] = true
	}
	for _, prefix := range del {
		if err := deleteDarwinOwnedRoute(ctx, prefix.String(), gateway.String()); err != nil {
			*old = owned
			return err
		}
		delete(owned, prefix)
	}
	*old = state
	return nil
}

func GetLocalInterface(context.Context) (LocalInterface, error) {
	out, err := exec.Command("route", "-n", "get", "default").CombinedOutput()
	if err != nil {
		return LocalInterface{}, fmt.Errorf("%w: %s", err, string(out))
	}
	var info LocalInterface
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "interface":
			info.Name = fields[1]
		case "gateway":
			info.Gateway = fields[1]
		}
	}
	if iface, err := net.InterfaceByName(info.Name); err == nil {
		info.MAC = iface.HardwareAddr.String()
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				info.IP4 = ipnet.IP.String()
			}
		}
		info.InterfaceIndex = iface.Index
	}
	if info.Name == "" || info.Gateway == "" {
		return LocalInterface{}, fmt.Errorf("incomplete local interface info")
	}
	return info, nil
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

func setDarwinDNS(ctx context.Context, servers []netip.Addr) error {
	if len(servers) == 0 {
		return clearDarwinDNS(ctx)
	}
	cmd := exec.CommandContext(ctx, "scutil")
	cmd.Stdin = strings.NewReader(darwinDNSSetScript(servers))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scutil DNS set failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func clearDarwinDNS(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "scutil")
	cmd.Stdin = strings.NewReader(darwinDNSClearScript())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scutil DNS clear failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func addrsToHostPrefixes(addrs []netip.Addr) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(addrs))
	for _, addr := range addrs {
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out
}
