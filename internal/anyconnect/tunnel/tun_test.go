package vpn

import (
	"context"
	"encoding/binary"
	"net/netip"
	"testing"

	"flexconnect/internal/anyconnect/base"
	"flexconnect/internal/anyconnect/session"
	"flexconnect/internal/osnet"
)

func TestBuildOSNetConfig(t *testing.T) {
	getLocalInterface = func(context.Context) (osnet.LocalInterface, error) {
		return osnet.LocalInterface{Gateway: "192.168.1.1", InterfaceIndex: 36}, nil
	}
	t.Cleanup(func() { getLocalInterface = osnet.GetLocalInterface })
	base.LocalInterface.Name = "Ethernet"
	base.LocalInterface.Gateway = "192.168.1.1"
	cSess := &session.ConnSession{
		TunName:       "FlexConnect",
		VPNAddress:    "172.20.144.185",
		VPNMask:       "255.255.240.0",
		MTU:           1399,
		ServerAddress: "222.66.117.109",
		SplitInclude:  []string{"172.16.0.0/12", "10.0.0.0/8"},
		SplitExclude:  []string{"203.0.113.0/24", "198.51.100.0/255.255.255.0"},
		DNS:           []string{"202.120.80.2", "202.120.81.2"},
	}
	cfg, err := buildOSNetConfig(cSess)
	if err != nil {
		t.Fatalf("buildOSNetConfig: %v", err)
	}
	if cfg.VPNAddress.String() != "172.20.144.185/20" {
		t.Fatalf("VPNAddress = %s", cfg.VPNAddress)
	}
	if cfg.ServerAddress.String() != "222.66.117.109" {
		t.Fatalf("ServerAddress = %s", cfg.ServerAddress)
	}
	wantInclude := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("10.0.0.0/8")}
	if len(cfg.IncludeRoutes) != len(wantInclude) {
		t.Fatalf("IncludeRoutes = %v", cfg.IncludeRoutes)
	}
	for i := range wantInclude {
		if cfg.IncludeRoutes[i] != wantInclude[i] {
			t.Fatalf("IncludeRoutes[%d] = %s, want %s", i, cfg.IncludeRoutes[i], wantInclude[i])
		}
	}
	if got := cfg.ExcludeRoutes[1].String(); got != "198.51.100.0/24" {
		t.Fatalf("ExcludeRoutes[1] = %s", got)
	}
	if len(cfg.DNSServers) != 2 || cfg.DNSServers[0].String() != "202.120.80.2" {
		t.Fatalf("DNSServers = %v", cfg.DNSServers)
	}
}

func TestUserFlowRoutesReversePacketsToUserDevice(t *testing.T) {
	router := newUserFlowRouter()
	outbound := testIPv4Packet(6, [4]byte{172, 20, 144, 185}, [4]byte{10, 64, 0, 7}, 53124, 443)
	inbound := testIPv4Packet(6, [4]byte{10, 64, 0, 7}, [4]byte{172, 20, 144, 185}, 443, 53124)

	if !router.recordOutbound(outbound) {
		t.Fatal("outbound user TCP packet was not recorded")
	}
	if !router.matchesInbound(inbound) {
		t.Fatal("reverse inbound TCP packet was not routed to user tunnel")
	}
}

func TestUserFlowDoesNotClaimUnmatchedPackets(t *testing.T) {
	router := newUserFlowRouter()
	outbound := testIPv4Packet(17, [4]byte{172, 20, 144, 185}, [4]byte{10, 64, 0, 53}, 53124, 53)
	unmatched := testIPv4Packet(17, [4]byte{10, 64, 0, 54}, [4]byte{172, 20, 144, 185}, 53, 53124)

	if !router.recordOutbound(outbound) {
		t.Fatal("outbound user UDP packet was not recorded")
	}
	if router.matchesInbound(unmatched) {
		t.Fatal("unmatched inbound UDP packet was incorrectly routed to user tunnel")
	}
}

func testIPv4Packet(proto byte, src, dst [4]byte, srcPort, dstPort uint16) []byte {
	packet := make([]byte, 40)
	packet[0] = 0x45
	packet[9] = proto
	copy(packet[12:16], src[:])
	copy(packet[16:20], dst[:])
	binary.BigEndian.PutUint16(packet[20:22], srcPort)
	binary.BigEndian.PutUint16(packet[22:24], dstPort)
	return packet
}

func TestBuildOSNetConfigAddsDefaultRouteWhenServerIncludesAreEmpty(t *testing.T) {
	getLocalInterface = func(context.Context) (osnet.LocalInterface, error) {
		return osnet.LocalInterface{}, nil
	}
	t.Cleanup(func() { getLocalInterface = osnet.GetLocalInterface })
	cSess := &session.ConnSession{
		TunName:                  "FlexConnect",
		VPNAddress:               "172.20.144.185",
		VPNMask:                  "255.255.255.255",
		UseDefaultRouteWhenEmpty: true,
	}
	cfg, err := buildOSNetConfig(cSess)
	if err != nil {
		t.Fatalf("buildOSNetConfig: %v", err)
	}
	if len(cfg.IncludeRoutes) != 1 || cfg.IncludeRoutes[0].String() != "0.0.0.0/0" {
		t.Fatalf("IncludeRoutes = %v", cfg.IncludeRoutes)
	}
}

func TestCollectDynamicRoutes(t *testing.T) {
	cSess := &session.ConnSession{}
	cSess.DynamicSplitIncludeResolved.Store("include.example", []string{"203.0.113.10"})
	cSess.DynamicSplitExcludeResolved.Store("exclude.example", []string{"198.51.100.20"})
	routes := collectDynamicRoutes(cSess)
	if len(routes.Include) != 1 || routes.Include[0].String() != "203.0.113.10" {
		t.Fatalf("Include = %v", routes.Include)
	}
	if len(routes.Exclude) != 1 || routes.Exclude[0].String() != "198.51.100.20" {
		t.Fatalf("Exclude = %v", routes.Exclude)
	}
}
