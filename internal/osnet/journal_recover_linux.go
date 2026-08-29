//go:build linux

package osnet

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os/exec"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/vishvananda/netlink"
)

func platformRecoverJournal(ctx context.Context, journal ownershipJournal) error {
	var errs []error
	var physical netlink.Link
	if journal.GatewayInterfaceIndex != 0 {
		physical, _ = netlink.LinkByIndex(journal.GatewayInterfaceIndex)
	}
	var gateway net.IP
	if journal.Gateway != "" {
		gateway = net.ParseIP(journal.Gateway)
	}
	physicalRoutes := append(routeStrings(journal.ServerAddress), journal.ExcludeRoutes...)
	physicalRoutes = append(physicalRoutes, hostRouteStrings(journal.DynamicExclude)...)
	for _, raw := range physicalRoutes {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := routeDel(prefix, physical, gateway, 5); err != nil {
			errs = append(errs, err)
		}
	}
	tunLink, _ := netlink.LinkByName(journal.InterfaceName)
	for _, raw := range append(journal.IncludeRoutes, hostRouteStrings(journal.DynamicInclude)...) {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if tunLink != nil {
			if err := routeDel(prefix, tunLink, nil, 6); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(journal.DNSServers) > 0 {
		if tunLink != nil {
			if conn, err := dbus.SystemBus(); err == nil {
				obj := conn.Object("org.freedesktop.resolve1", dbus.ObjectPath("/org/freedesktop/resolve1"))
				_ = obj.Call("org.freedesktop.resolve1.Manager.RevertLink", 0, int32(tunLink.Attrs().Index)).Err
				conn.Close()
			}
		}
		if path, err := exec.LookPath("resolvconf"); err == nil {
			cmd := exec.CommandContext(ctx, path, "-d", "flexconnect."+journal.InterfaceName)
			if out, err := cmd.CombinedOutput(); err != nil && !strings.Contains(strings.ToLower(string(out)), "not found") {
				errs = append(errs, err)
			}
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
			continue
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()).String())
	}
	return out
}
