//go:build windows

package osnet

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// windowsDefaultRoute is the usable IPv4 underlay selected from the Windows
// routing table. Windows compares a route's metric plus its interface metric;
// comparing MIB_IPFORWARD_ROW2.Metric alone can select a disconnected adapter
// that still has a stale default route.
type windowsDefaultRoute struct {
	interfaceName   string
	interfaceIndex  int
	localIPv4       netip.Addr
	mac             string
	gateway         netip.Addr
	effectiveMetric int
}

type windowsRouteCandidate struct {
	interfaceName   string
	interfaceIndex  int
	localIPv4       netip.Addr
	mac             string
	gateway         netip.Addr
	routeMetric     uint32
	interfaceMetric uint32
	operational     bool
	ipv4Enabled     bool
	loopback        bool
}

func getWindowsDefaultRoute(excludeInterface string) (windowsDefaultRoute, error) {
	routes, err := winipcfg.GetIPForwardTable2(windows.AF_INET)
	if err != nil {
		return windowsDefaultRoute{}, fmt.Errorf("read IPv4 route table: %w", err)
	}
	adapters, err := winipcfg.GetAdaptersAddresses(windows.AF_INET, winipcfg.GAAFlagIncludeAllInterfaces)
	if err != nil {
		return windowsDefaultRoute{}, fmt.Errorf("read IPv4 adapters: %w", err)
	}

	byLUID := make(map[winipcfg.LUID]*winipcfg.IPAdapterAddresses, len(adapters))
	for _, adapter := range adapters {
		byLUID[adapter.LUID] = adapter
	}

	candidates := make([]windowsRouteCandidate, 0, len(routes))
	for i := range routes {
		route := &routes[i]
		destination := route.DestinationPrefix.Prefix()
		if route.Loopback || destination.Bits() != 0 || destination.Addr().Unmap() != netip.IPv4Unspecified() {
			continue
		}
		adapter := byLUID[route.InterfaceLUID]
		if adapter == nil {
			continue
		}
		localIPv4 := firstUsableWindowsIPv4(adapter)
		mac := ""
		if physical := adapter.PhysicalAddress(); len(physical) > 0 {
			mac = net.HardwareAddr(physical).String()
		}
		candidates = append(candidates, windowsRouteCandidate{
			interfaceName:   adapter.FriendlyName(),
			interfaceIndex:  int(adapter.IfIndex),
			localIPv4:       localIPv4,
			mac:             mac,
			gateway:         route.NextHop.Addr().Unmap(),
			routeMetric:     route.Metric,
			interfaceMetric: adapter.Ipv4Metric,
			operational:     adapter.OperStatus == winipcfg.IfOperStatusUp,
			ipv4Enabled:     adapter.Flags&winipcfg.IPAAFlagIpv4Enabled != 0,
			loopback:        adapter.IfType == winipcfg.IfTypeSoftwareLoopback,
		})
	}

	selected, err := selectWindowsDefaultRoute(candidates, excludeInterface)
	if err != nil {
		return windowsDefaultRoute{}, fmt.Errorf("select IPv4 underlay from %d default routes: %w", len(candidates), err)
	}
	metric := uint64(selected.routeMetric) + uint64(selected.interfaceMetric)
	if metric > uint64(math.MaxInt) {
		return windowsDefaultRoute{}, errors.New("selected IPv4 underlay metric overflows int")
	}
	return windowsDefaultRoute{
		interfaceName:   selected.interfaceName,
		interfaceIndex:  selected.interfaceIndex,
		localIPv4:       selected.localIPv4,
		mac:             selected.mac,
		gateway:         selected.gateway,
		effectiveMetric: int(metric),
	}, nil
}

func firstUsableWindowsIPv4(adapter *winipcfg.IPAdapterAddresses) netip.Addr {
	for address := adapter.FirstUnicastAddress; address != nil; address = address.Next {
		ip := address.Address.IP()
		if ip == nil || ip.To4() == nil {
			continue
		}
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		// A link-local address cannot be a valid source for an Internet default
		// route. Reject it here so the caller gets a useful route-selection
		// error instead of a later WSAEADDRNOTAVAIL bind failure.
		if addr.IsGlobalUnicast() {
			return addr
		}
	}
	return netip.Addr{}
}

func selectWindowsDefaultRoute(candidates []windowsRouteCandidate, excludeInterface string) (windowsRouteCandidate, error) {
	bestIndex := -1
	bestMetric := uint64(math.MaxUint64)
	for i := range candidates {
		candidate := &candidates[i]
		if !candidate.operational || !candidate.ipv4Enabled || candidate.loopback ||
			candidate.interfaceName == excludeInterface || !candidate.localIPv4.IsGlobalUnicast() {
			continue
		}
		metric := uint64(candidate.routeMetric) + uint64(candidate.interfaceMetric)
		if bestIndex == -1 || metric < bestMetric ||
			(metric == bestMetric && candidate.interfaceIndex < candidates[bestIndex].interfaceIndex) {
			bestIndex = i
			bestMetric = metric
		}
	}
	if bestIndex == -1 {
		return windowsRouteCandidate{}, errors.New("no operational IPv4 default route with a usable source address")
	}
	return candidates[bestIndex], nil
}
