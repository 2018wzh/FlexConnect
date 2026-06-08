//go:build linux

package osnet

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"

	wgtun "github.com/tailscale/wireguard-go/tun"
	"github.com/vishvananda/netlink"
)

type platformManager struct {
	name           string
	link           netlink.Link
	localLink      netlink.Link
	gateway        net.IP
	serverRoutes   map[netip.Prefix]bool
	includeRoutes  map[netip.Prefix]bool
	excludeRoutes  map[netip.Prefix]bool
	dynamicInclude map[netip.Prefix]bool
	dynamicExclude map[netip.Prefix]bool
	dnsOriginal    []byte
	dnsBackedUp    bool
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

func (m *platformManager) Up(context.Context) error {
	link, err := netlink.LinkByName(m.name)
	if err != nil {
		return err
	}
	m.link = link
	if err := netlink.LinkSetUp(link); err != nil {
		return err
	}
	_ = netlink.LinkSetMulticastOff(link)
	return nil
}

func (m *platformManager) Set(ctx context.Context, cfg *Config) error {
	if cfg == nil || !cfg.VPNAddress.IsValid() {
		return m.Close(ctx)
	}
	if m.link == nil {
		if err := m.Up(ctx); err != nil {
			return err
		}
	}
	if cfg.MTU > 0 {
		if err := netlink.LinkSetMTU(m.link, cfg.MTU); err != nil {
			return err
		}
	}
	addr, err := netlink.ParseAddr(cfg.VPNAddress.String())
	if err != nil {
		return err
	}
	_ = netlink.AddrReplace(m.link, addr)

	if cfg.GatewayInterfaceIndex != 0 {
		m.localLink, _ = netlink.LinkByIndex(cfg.GatewayInterfaceIndex)
	}
	if m.localLink == nil {
		m.localLink = m.link
	}
	if cfg.Gateway.IsValid() {
		m.gateway = net.ParseIP(cfg.Gateway.String())
	}

	var server []netip.Prefix
	if cfg.ServerAddress.IsValid() {
		server = append(server, netip.PrefixFrom(cfg.ServerAddress, cfg.ServerAddress.BitLen()))
	}
	if err := m.syncRoutes(&m.serverRoutes, server, m.localLink, m.gateway, 5); err != nil {
		return err
	}
	if err := m.syncRoutes(&m.includeRoutes, cfg.IncludeRoutes, m.link, nil, 6); err != nil {
		return err
	}
	if err := m.syncRoutes(&m.excludeRoutes, cfg.ExcludeRoutes, m.localLink, m.gateway, 5); err != nil {
		return err
	}
	if err := m.setDNS(ctx, cfg.DNSServers); err != nil {
		return err
	}
	return nil
}

func (m *platformManager) SetDynamicRoutes(_ context.Context, routes DynamicRoutes) error {
	if err := m.syncRoutes(&m.dynamicInclude, addrsToHostPrefixes(routes.Include), m.link, nil, 6); err != nil {
		return err
	}
	return m.syncRoutes(&m.dynamicExclude, addrsToHostPrefixes(routes.Exclude), m.localLink, m.gateway, 5)
}

func (m *platformManager) Close(ctx context.Context) error {
	var firstErr error
	groups := []struct {
		routes *map[netip.Prefix]bool
		link   netlink.Link
	}{
		{&m.serverRoutes, m.localLink},
		{&m.includeRoutes, m.link},
		{&m.excludeRoutes, m.localLink},
		{&m.dynamicInclude, m.link},
		{&m.dynamicExclude, m.localLink},
	}
	for _, group := range groups {
		for prefix := range *group.routes {
			if err := routeDel(prefix, group.link); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		*group.routes = map[netip.Prefix]bool{}
	}
	if err := m.restoreDNS(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (m *platformManager) syncRoutes(old *map[netip.Prefix]bool, next []netip.Prefix, link netlink.Link, gw net.IP, priority int) error {
	if link == nil {
		return nil
	}
	add, del, state := DiffPrefixes(*old, next)
	for _, prefix := range del {
		if err := routeDel(prefix, link); err != nil {
			return err
		}
	}
	for _, prefix := range add {
		dst, err := netlink.ParseIPNet(prefix.String())
		if err != nil {
			return err
		}
		route := &netlink.Route{LinkIndex: link.Attrs().Index, Dst: dst, Gw: gw, Priority: priority}
		if err := netlink.RouteReplace(route); err != nil {
			return err
		}
	}
	*old = state
	return nil
}

func routeDel(prefix netip.Prefix, link netlink.Link) error {
	if link == nil {
		return fmt.Errorf("missing link for route cleanup %s", prefix)
	}
	dst, err := netlink.ParseIPNet(prefix.String())
	if err != nil {
		return err
	}
	filter := &netlink.Route{Dst: dst, LinkIndex: link.Attrs().Index}
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, filter, netlink.RT_FILTER_DST|netlink.RT_FILTER_OIF)
	if err != nil {
		return err
	}
	for _, route := range routes {
		_ = netlink.RouteDel(&route)
	}
	return nil
}

func GetLocalInterface(context.Context) (LocalInterface, error) {
	routes, err := netlink.RouteGet(net.ParseIP("8.8.8.8"))
	if err != nil {
		return LocalInterface{}, err
	}
	if len(routes) == 0 {
		return LocalInterface{}, fmt.Errorf("no default IPv4 route")
	}
	route := routes[0]
	link, err := netlink.LinkByIndex(route.LinkIndex)
	if err != nil {
		return LocalInterface{}, err
	}
	return LocalInterface{
		Name:           link.Attrs().Name,
		IP4:            route.Src.String(),
		MAC:            link.Attrs().HardwareAddr.String(),
		Gateway:        route.Gw.String(),
		InterfaceIndex: route.LinkIndex,
	}, nil
}

func (m *platformManager) setDNS(ctx context.Context, servers []netip.Addr) error {
	if len(servers) == 0 {
		return m.restoreDNS(ctx)
	}
	if _, err := exec.LookPath("resolvectl"); err == nil {
		args := append([]string{"dns", m.name}, AddrStrings(servers)...)
		cmd := exec.CommandContext(ctx, "resolvectl", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, string(out))
		}
		return nil
	}
	if !m.dnsBackedUp {
		data, err := os.ReadFile("/etc/resolv.conf")
		if err != nil {
			return err
		}
		m.dnsOriginal = append([]byte(nil), data...)
		m.dnsBackedUp = true
	}
	var b strings.Builder
	for _, server := range servers {
		fmt.Fprintf(&b, "nameserver %s\n", server)
	}
	return os.WriteFile("/etc/resolv.conf", []byte(b.String()), 0o644)
}

func (m *platformManager) restoreDNS(ctx context.Context) error {
	if _, err := exec.LookPath("resolvectl"); err == nil {
		cmd := exec.CommandContext(ctx, "resolvectl", "revert", m.name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, string(out))
		}
		return nil
	}
	if !m.dnsBackedUp {
		return nil
	}
	err := os.WriteFile("/etc/resolv.conf", m.dnsOriginal, 0o644)
	m.dnsBackedUp = false
	m.dnsOriginal = nil
	return err
}

func addrsToHostPrefixes(addrs []netip.Addr) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(addrs))
	for _, addr := range addrs {
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out
}
