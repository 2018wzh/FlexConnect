//go:build linux

package osnet

import (
	"context"
	"net"
	"net/netip"
	"os"
	"runtime"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

func TestNetworkNamespaceRouteOwnershipTransaction(t *testing.T) {
	if os.Getenv("FLEXCONNECT_NETNS_TEST") != "1" {
		t.Skip("set FLEXCONNECT_NETNS_TEST=1 and run as root")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	original, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	testNS, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := netns.Set(original); err != nil {
			t.Errorf("restore namespace: %v", err)
		}
		testNS.Close()
	}()

	physical := addDummyLink(t, "fcphy0", "192.0.2.1/24")
	tunnel := addDummyLink(t, "fctun0", "10.0.0.1/24")
	destination := mustIPNet(t, "10.10.0.0/16")
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: physical.Attrs().Index, Dst: destination, Priority: 99}); err != nil {
		t.Fatal(err)
	}

	managerValue, err := newPlatformManager(nil, tunnel.Attrs().Name)
	if err != nil {
		t.Fatal(err)
	}
	manager := managerValue.(*platformManager)
	cfg := &Config{
		InterfaceName: tunnel.Attrs().Name, VPNAddress: netip.MustParsePrefix("10.0.0.2/32"), MTU: 1399,
		Gateway: netip.MustParseAddr("192.0.2.254"), GatewayInterfaceIndex: physical.Attrs().Index,
		ServerAddress: netip.MustParseAddr("198.51.100.10"), IncludeRoutes: []netip.Prefix{netip.MustParsePrefix("10.10.0.0/16")},
		ExcludeRoutes: []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")},
	}
	if err := manager.Set(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	owned := findRoute(t, destination, tunnel.Attrs().Index, 6)
	if err := netlink.RouteDel(&owned); err != nil {
		t.Fatal(err)
	}
	external := netlink.Route{LinkIndex: tunnel.Attrs().Index, Dst: destination, Priority: 42}
	if err := netlink.RouteAdd(&external); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = findRoute(t, destination, physical.Attrs().Index, 99)
	_ = findRoute(t, destination, tunnel.Attrs().Index, 42)
}

func addDummyLink(t *testing.T, name, cidr string) netlink.Link {
	t.Helper()
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	actual, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(actual, addr); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(actual); err != nil {
		t.Fatal(err)
	}
	return actual
}

func mustIPNet(t *testing.T, raw string) *net.IPNet {
	t.Helper()
	_, value, err := net.ParseCIDR(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func findRoute(t *testing.T, destination *net.IPNet, linkIndex, priority int) netlink.Route {
	t.Helper()
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Dst: destination, LinkIndex: linkIndex}, netlink.RT_FILTER_DST|netlink.RT_FILTER_OIF)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.Priority == priority {
			return route
		}
	}
	t.Fatalf("route %s link=%d priority=%d not found: %+v", destination, linkIndex, priority, routes)
	return netlink.Route{}
}
