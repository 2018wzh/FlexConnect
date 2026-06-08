//go:build windows

package osnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"

	wgtun "github.com/tailscale/wireguard-go/tun"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

type luidProvider interface {
	LUID() uint64
}

type platformManager struct {
	name         string
	tunLUID      winipcfg.LUID
	gatewayLUID  winipcfg.LUID
	gateway      netip.Addr
	serverRoutes map[netip.Prefix]bool
	routes       map[netip.Prefix]bool
	exclude      map[netip.Prefix]bool
	dynamicIn    map[netip.Prefix]bool
	dynamicEx    map[netip.Prefix]bool
}

func newPlatformManager(dev wgtun.Device, name string) (Manager, error) {
	withLUID, ok := dev.(luidProvider)
	if !ok {
		return nil, errors.New("Windows TUN device does not expose LUID")
	}
	return &platformManager{
		name:         name,
		tunLUID:      winipcfg.LUID(withLUID.LUID()),
		serverRoutes: map[netip.Prefix]bool{},
		routes:       map[netip.Prefix]bool{},
		exclude:      map[netip.Prefix]bool{},
		dynamicIn:    map[netip.Prefix]bool{},
		dynamicEx:    map[netip.Prefix]bool{},
	}, nil
}

func (m *platformManager) Up(context.Context) error {
	if _, err := m.tunLUID.Interface(); err != nil {
		return err
	}
	return nil
}

func (m *platformManager) Set(ctx context.Context, cfg *Config) error {
	if cfg == nil || !cfg.VPNAddress.IsValid() {
		return m.Close(ctx)
	}
	if cfg.Gateway.IsValid() {
		m.gateway = cfg.Gateway
	}
	if cfg.GatewayInterfaceIndex != 0 {
		luid, err := winipcfg.LUIDFromIndex(uint32(cfg.GatewayInterfaceIndex))
		if err != nil {
			return err
		}
		m.gatewayLUID = luid
	}
	if err := m.tunLUID.SetIPAddressesForFamily(windows.AF_INET, []netip.Prefix{cfg.VPNAddress}); err != nil {
		return err
	}
	if cfg.MTU > 0 {
		iface, err := m.tunLUID.IPInterface(windows.AF_INET)
		if err != nil {
			return err
		}
		iface.NLMTU = uint32(cfg.MTU)
		iface.UseAutomaticMetric = false
		iface.Metric = 0
		if err := iface.Set(); err != nil {
			return err
		}
	}
	var server []netip.Prefix
	if cfg.ServerAddress.IsValid() {
		server = append(server, netip.PrefixFrom(cfg.ServerAddress, cfg.ServerAddress.BitLen()))
	}
	if err := m.syncRoutes(&m.serverRoutes, server, m.gatewayLUID, m.gateway, 5); err != nil {
		return err
	}
	if err := m.syncRoutes(&m.routes, cfg.IncludeRoutes, m.tunLUID, cfg.VPNAddress.Addr(), 6); err != nil {
		return err
	}
	if err := m.syncRoutes(&m.exclude, cfg.ExcludeRoutes, m.gatewayLUID, m.gateway, 5); err != nil {
		return err
	}
	if err := m.setDNS(cfg.DNSServers); err != nil {
		return err
	}
	return nil
}

func (m *platformManager) SetDynamicRoutes(_ context.Context, routes DynamicRoutes) error {
	if err := m.syncRoutes(&m.dynamicIn, addrsToHostPrefixes(routes.Include), m.tunLUID, netip.IPv4Unspecified(), 6); err != nil {
		return err
	}
	return m.syncRoutes(&m.dynamicEx, addrsToHostPrefixes(routes.Exclude), m.gatewayLUID, m.gateway, 5)
}

func (m *platformManager) Close(context.Context) error {
	var firstErr error
	groups := []struct {
		routes *map[netip.Prefix]bool
		luid   winipcfg.LUID
		next   netip.Addr
	}{
		{&m.serverRoutes, m.gatewayLUID, m.gateway},
		{&m.routes, m.tunLUID, netip.IPv4Unspecified()},
		{&m.exclude, m.gatewayLUID, m.gateway},
		{&m.dynamicIn, m.tunLUID, netip.IPv4Unspecified()},
		{&m.dynamicEx, m.gatewayLUID, m.gateway},
	}
	for _, group := range groups {
		for prefix := range *group.routes {
			if err := deleteRoute(group.luid, prefix, group.next); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		*group.routes = map[netip.Prefix]bool{}
	}
	if err := m.tunLUID.FlushDNS(windows.AF_INET); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (m *platformManager) syncRoutes(old *map[netip.Prefix]bool, next []netip.Prefix, luid winipcfg.LUID, nextHop netip.Addr, metric uint32) error {
	if luid == 0 && len(next) > 0 {
		return fmt.Errorf("missing interface LUID for route sync")
	}
	add, del, state := DiffPrefixes(*old, next)
	for _, prefix := range del {
		if err := deleteRoute(luid, prefix, nextHop); err != nil {
			return err
		}
	}
	for _, prefix := range add {
		if err := luid.AddRoute(prefix, nextHop, metric); err != nil && !isNotFoundOrExists(err) {
			return err
		}
	}
	*old = state
	return nil
}

func deleteRoute(luid winipcfg.LUID, prefix netip.Prefix, nextHop netip.Addr) error {
	if luid == 0 {
		return fmt.Errorf("missing interface LUID for route cleanup %s", prefix)
	}
	err := luid.DeleteRoute(prefix, nextHop)
	if isNotFoundOrExists(err) {
		return nil
	}
	return err
}

func (m *platformManager) setDNS(servers []netip.Addr) error {
	if len(servers) == 0 {
		return m.tunLUID.FlushDNS(windows.AF_INET)
	}
	return m.tunLUID.SetDNS(windows.AF_INET, servers, nil)
}

func GetLocalInterface(context.Context) (LocalInterface, error) {
	routes, err := winipcfg.GetIPForwardTable2(windows.AF_INET)
	if err != nil {
		return LocalInterface{}, err
	}
	var best *winipcfg.MibIPforwardRow2
	for i := range routes {
		route := &routes[i]
		if route.DestinationPrefix.PrefixLength != 0 {
			continue
		}
		if best == nil || route.Metric < best.Metric {
			best = route
		}
	}
	if best == nil {
		return LocalInterface{}, errors.New("no default IPv4 route")
	}
	adapters, err := winipcfg.GetAdaptersAddresses(windows.AF_INET, winipcfg.GAAFlagIncludeAllInterfaces)
	if err != nil {
		return LocalInterface{}, err
	}
	for _, adapter := range adapters {
		if adapter.LUID != best.InterfaceLUID {
			continue
		}
		info := LocalInterface{
			Name:           adapter.FriendlyName(),
			Gateway:        best.NextHop.Addr().Unmap().String(),
			InterfaceIndex: int(adapter.IfIndex),
		}
		if len(adapter.PhysicalAddress()) > 0 {
			info.MAC = net.HardwareAddr(adapter.PhysicalAddress()).String()
		}
		for addr := adapter.FirstUnicastAddress; addr != nil; addr = addr.Next {
			if ip := addr.Address.IP(); ip != nil && ip.To4() != nil {
				info.IP4 = ip.String()
				break
			}
		}
		return info, nil
	}
	return LocalInterface{}, errors.New("default route interface not found")
}

func isNotFoundOrExists(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) ||
		errors.Is(err, windows.ERROR_ALREADY_EXISTS)
}

func addrsToHostPrefixes(addrs []netip.Addr) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(addrs))
	for _, addr := range addrs {
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out
}
