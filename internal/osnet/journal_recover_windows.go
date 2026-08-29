//go:build windows

package osnet

import (
	"context"
	"errors"
	"net/netip"

	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func platformRecoverJournal(_ context.Context, journal ownershipJournal) error {
	// A Wintun adapter and its DNS state are handle-owned and disappear when a
	// crashed daemon exits. Physical underlay routes are the only persistent
	// objects requiring explicit recovery.
	return recoverWindowsPhysicalRoutes(journal)
}

func recoverWindowsPhysicalRoutes(journal ownershipJournal) error {
	if journal.GatewayInterfaceIndex == 0 {
		return nil
	}
	luid, err := winipcfg.LUIDFromIndex(uint32(journal.GatewayInterfaceIndex))
	if err != nil {
		return err
	}
	gateway, err := netip.ParseAddr(journal.Gateway)
	if err != nil {
		return err
	}
	var errs []error
	routes := append(routeStrings(journal.ServerAddress), journal.ExcludeRoutes...)
	routes = append(routes, hostRouteStrings(journal.DynamicExclude)...)
	for _, raw := range routes {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := deleteRoute(luid, prefix, gateway, 5); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func routeStrings(address string) []string {
	if address == "" {
		return nil
	}
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return []string{address}
	}
	return []string{netip.PrefixFrom(addr, addr.BitLen()).String()}
}

func hostRouteStrings(addresses []string) []string {
	out := make([]string, 0, len(addresses))
	for _, raw := range addresses {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			out = append(out, raw)
		} else {
			out = append(out, netip.PrefixFrom(addr, addr.BitLen()).String())
		}
	}
	return out
}
