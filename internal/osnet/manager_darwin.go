//go:build darwin

package osnet

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"

	wgtun "github.com/tailscale/wireguard-go/tun"
)

type platformManager struct {
	name           string
	gateway        netip.Addr
	vpnAddr        netip.Addr
	serverRoutes   map[netip.Prefix]bool
	includeRoutes  map[netip.Prefix]bool
	excludeRoutes  map[netip.Prefix]bool
	dynamicInclude map[netip.Prefix]bool
	dynamicExclude map[netip.Prefix]bool
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
	m.vpnAddr = cfg.VPNAddress.Addr()
	m.gateway = cfg.Gateway
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
	if err := m.syncRoutes(ctx, &m.excludeRoutes, cfg.ExcludeRoutes, m.gateway); err != nil {
		return err
	}
	if len(cfg.DNSServers) > 0 {
		servers := AddrStrings(cfg.DNSServers)
		_ = runShell(ctx, fmt.Sprintf("networksetup -setdnsservers %s %s", shellQuote(m.name), strings.Join(servers, " ")))
	}
	return nil
}

func (m *platformManager) SetDynamicRoutes(ctx context.Context, routes DynamicRoutes) error {
	if err := m.syncRoutes(ctx, &m.dynamicInclude, addrsToHostPrefixes(routes.Include), m.vpnAddr); err != nil {
		return err
	}
	return m.syncRoutes(ctx, &m.dynamicExclude, addrsToHostPrefixes(routes.Exclude), m.gateway)
}

func (m *platformManager) Close(ctx context.Context) error {
	for _, routes := range []*map[netip.Prefix]bool{&m.serverRoutes, &m.includeRoutes, &m.excludeRoutes, &m.dynamicInclude, &m.dynamicExclude} {
		for prefix := range *routes {
			_ = run(ctx, "route", "delete", "-net", prefix.String())
		}
		*routes = map[netip.Prefix]bool{}
	}
	_ = runShell(ctx, fmt.Sprintf("networksetup -setdnsservers %s empty", shellQuote(m.name)))
	return nil
}

func (m *platformManager) syncRoutes(ctx context.Context, old *map[netip.Prefix]bool, next []netip.Prefix, gateway netip.Addr) error {
	if !gateway.IsValid() {
		return nil
	}
	add, del, state := DiffPrefixes(*old, next)
	for _, prefix := range del {
		_ = run(ctx, "route", "delete", "-net", prefix.String())
	}
	for _, prefix := range add {
		if err := run(ctx, "route", "add", "-net", prefix.String(), gateway.String()); err != nil {
			return err
		}
	}
	*old = state
	return nil
}

func GetLocalInterface(context.Context) (LocalInterface, error) {
	out, err := exec.Command("route", "-n", "get", "8.8.8.8").CombinedOutput()
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

func runShell(ctx context.Context, script string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func addrsToHostPrefixes(addrs []netip.Addr) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(addrs))
	for _, addr := range addrs {
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out
}
