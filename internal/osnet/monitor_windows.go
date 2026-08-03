//go:build windows

package osnet

import (
	"context"
	"errors"
	"io"
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func GetUnderlaySnapshot(ctx context.Context, excludeInterface string) (UnderlaySnapshot, error) {
	if ctx == nil {
		return UnderlaySnapshot{}, errors.New("nil underlay context")
	}
	routes, err := winipcfg.GetIPForwardTable2(windows.AF_INET)
	if err != nil {
		return UnderlaySnapshot{}, err
	}
	adapters, err := winipcfg.GetAdaptersAddresses(windows.AF_INET, winipcfg.GAAFlagIncludeAllInterfaces)
	if err != nil {
		return UnderlaySnapshot{}, err
	}
	var best *winipcfg.MibIPforwardRow2
	for i := range routes {
		route := &routes[i]
		if route.DestinationPrefix.PrefixLength != 0 {
			continue
		}
		for _, adapter := range adapters {
			if adapter.LUID != route.InterfaceLUID || adapter.FriendlyName() == excludeInterface {
				continue
			}
			if best == nil || route.Metric < best.Metric {
				copy := *route
				best = &copy
			}
		}
	}
	if best == nil {
		return UnderlaySnapshot{}, errors.New("no physical default IPv4 route")
	}
	for _, adapter := range adapters {
		if adapter.LUID != best.InterfaceLUID {
			continue
		}
		var local netip.Addr
		for addr := adapter.FirstUnicastAddress; addr != nil; addr = addr.Next {
			if ip := addr.Address.IP(); ip != nil && ip.To4() != nil {
				local = netip.MustParseAddr(ip.String()).Unmap()
				break
			}
		}
		if !local.IsValid() {
			return UnderlaySnapshot{}, errors.New("physical default interface has no IPv4 address")
		}
		return UnderlaySnapshot{
			InterfaceName:    adapter.FriendlyName(),
			InterfaceIndex:   int(adapter.IfIndex),
			LocalIPv4:        local,
			Gateway:          best.NextHop.Addr().Unmap(),
			GatewayInterface: int(adapter.IfIndex),
			RouteMetric:      int(best.Metric),
		}, nil
	}
	return UnderlaySnapshot{}, errors.New("physical default route interface not found")
}

func newUnderlayNotifier(trigger func()) (io.Closer, error) {
	return newWindowsUnderlayNotifier(trigger)
}
