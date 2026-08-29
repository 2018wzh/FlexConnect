//go:build windows

package osnet

import (
	"net/netip"
	"strings"
	"testing"
)

func TestSelectWindowsDefaultRouteUsesEffectiveMetric(t *testing.T) {
	candidates := []windowsRouteCandidate{
		{
			interfaceName: "stale-wifi", interfaceIndex: 19,
			localIPv4:   netip.MustParseAddr("169.254.2.10"),
			routeMetric: 0, interfaceMetric: 45,
			operational: false, ipv4Enabled: true,
		},
		{
			interfaceName: "ethernet", interfaceIndex: 16,
			localIPv4:   netip.MustParseAddr("192.168.1.28"),
			routeMetric: 0, interfaceMetric: 25,
			operational: true, ipv4Enabled: true,
		},
		{
			interfaceName: "upstream-vpn", interfaceIndex: 78,
			localIPv4:   netip.MustParseAddr("198.18.0.1"),
			routeMetric: 5, interfaceMetric: 5,
			operational: true, ipv4Enabled: true,
		},
	}

	selected, err := selectWindowsDefaultRoute(candidates, "")
	if err != nil {
		t.Fatal(err)
	}
	if selected.interfaceName != "upstream-vpn" {
		t.Fatalf("selected interface = %q, want upstream-vpn", selected.interfaceName)
	}
}

func TestSelectWindowsDefaultRouteExcludesTunnelAndUnusableAdapters(t *testing.T) {
	candidates := []windowsRouteCandidate{
		{
			interfaceName: "FlexConnect", interfaceIndex: 8,
			localIPv4:   netip.MustParseAddr("10.0.0.2"),
			operational: true, ipv4Enabled: true,
		},
		{
			interfaceName: "link-local", interfaceIndex: 19,
			localIPv4:   netip.MustParseAddr("169.254.2.10"),
			operational: true, ipv4Enabled: true,
		},
		{
			interfaceName: "ethernet", interfaceIndex: 16,
			localIPv4:   netip.MustParseAddr("192.168.1.28"),
			routeMetric: 1, interfaceMetric: 25,
			operational: true, ipv4Enabled: true,
		},
	}

	selected, err := selectWindowsDefaultRoute(candidates, "FlexConnect")
	if err != nil {
		t.Fatal(err)
	}
	if selected.interfaceName != "ethernet" {
		t.Fatalf("selected interface = %q, want ethernet", selected.interfaceName)
	}
}

func TestSelectWindowsDefaultRouteFailsWhenNoUsableSourceExists(t *testing.T) {
	_, err := selectWindowsDefaultRoute([]windowsRouteCandidate{{
		interfaceName: "disconnected", operational: false, ipv4Enabled: true,
	}}, "")
	if err == nil || !strings.Contains(err.Error(), "no operational IPv4 default route") {
		t.Fatalf("unexpected error: %v", err)
	}
}
