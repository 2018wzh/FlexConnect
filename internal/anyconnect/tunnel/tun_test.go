package vpn

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/netip"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"flexconnect/internal/anyconnect/auth"
	"flexconnect/internal/anyconnect/proto"
	"flexconnect/internal/anyconnect/session"
	"flexconnect/internal/osnet"
	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	wgtun "github.com/tailscale/wireguard-go/tun"
)

type testTUNDevice struct {
	events    chan wgtun.Event
	readErr   error
	writeErr  error
	closeOnce sync.Once
}

func dnsResponsePacket(t *testing.T, questions []layers.DNSQuestion, answers []layers.DNSResourceRecord) []byte {
	t.Helper()
	ip := &layers.IPv4{Version: 4, IHL: 5, TTL: 64, Protocol: layers.IPProtocolUDP, SrcIP: []byte{10, 0, 0, 53}, DstIP: []byte{10, 0, 0, 2}}
	udp := &layers.UDP{SrcPort: 53, DstPort: 53000}
	if err := udp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatal(err)
	}
	dns := &layers.DNS{ID: 1, QR: true, Questions: questions, Answers: answers, QDCount: uint16(len(questions)), ANCount: uint16(len(answers))}
	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}, ip, udp, dns); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestDynamicRoutesValidateDomainBoundaryAndClampTTL(t *testing.T) {
	cSess := (&session.Session{}).NewConnSession(&http.Header{})
	cSess.DynamicSplitIncludeDomains = []string{"example.com"}
	query := []byte("vpn.example.com")
	packet := dnsResponsePacket(t, []layers.DNSQuestion{{Name: query, Type: layers.DNSTypeA, Class: layers.DNSClassIN}}, []layers.DNSResourceRecord{{Name: query, Type: layers.DNSTypeA, Class: layers.DNSClassIN, TTL: 1, IP: []byte{203, 0, 113, 10}}})
	if !dynamicSplitRoutes(packet, cSess) {
		t.Fatal("valid matching response was rejected")
	}
	value, ok := cSess.DynamicSplitIncludeResolved.Load("vpn.example.com")
	if !ok {
		t.Fatal("lease missing")
	}
	lease := value.(dynamicRouteLease)
	remaining := time.Until(lease.ExpiresAt)
	if remaining < minimumDynamicRouteTTL-time.Second || remaining > minimumDynamicRouteTTL+time.Second {
		t.Fatalf("TTL = %s", remaining)
	}

	bad := []byte("notexample.com")
	packet = dnsResponsePacket(t, []layers.DNSQuestion{{Name: bad, Type: layers.DNSTypeA, Class: layers.DNSClassIN}}, []layers.DNSResourceRecord{{Name: bad, Type: layers.DNSTypeA, Class: layers.DNSClassIN, TTL: 60, IP: []byte{198, 51, 100, 1}}})
	if dynamicSplitRoutes(packet, cSess) {
		t.Fatal("non-label suffix matched")
	}
}

func TestDynamicRoutesRejectAnswerForUnrelatedNameAndZeroQuestion(t *testing.T) {
	cSess := (&session.Session{}).NewConnSession(&http.Header{})
	cSess.DynamicSplitIncludeDomains = []string{"example.com"}
	query := []byte("vpn.example.com")
	unrelated := []byte("attacker.example")
	packet := dnsResponsePacket(t, []layers.DNSQuestion{{Name: query, Type: layers.DNSTypeA, Class: layers.DNSClassIN}}, []layers.DNSResourceRecord{{Name: unrelated, Type: layers.DNSTypeA, Class: layers.DNSClassIN, TTL: 60, IP: []byte{203, 0, 113, 10}}})
	if dynamicSplitRoutes(packet, cSess) {
		t.Fatal("unrelated answer was accepted")
	}
	if dynamicSplitRoutes(dnsResponsePacket(t, nil, nil), cSess) {
		t.Fatal("zero-question response was accepted")
	}
}

func TestDynamicRoutesRejectUnsupportedQuestionAndLimitOverflow(t *testing.T) {
	cSess := (&session.Session{}).NewConnSession(&http.Header{})
	cSess.DynamicSplitIncludeDomains = []string{"example.com"}
	query := []byte("vpn.example.com")
	packet := dnsResponsePacket(t, []layers.DNSQuestion{{Name: query, Type: layers.DNSTypeAAAA, Class: layers.DNSClassIN}}, nil)
	if dynamicSplitRoutes(packet, cSess) {
		t.Fatal("AAAA question was accepted by the IPv4 route reconciler")
	}
	for i := 0; i < maximumDynamicRoutes; i++ {
		addr := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		cSess.DynamicSplitIncludeResolved.Store(addr.String(), dynamicRouteLease{Addresses: []string{addr.String()}, ExpiresAt: time.Now().Add(time.Minute)})
	}
	packet = dnsResponsePacket(t,
		[]layers.DNSQuestion{{Name: query, Type: layers.DNSTypeA, Class: layers.DNSClassIN}},
		[]layers.DNSResourceRecord{{Name: query, Type: layers.DNSTypeA, Class: layers.DNSClassIN, TTL: 60, IP: []byte{203, 0, 113, 10}}},
	)
	if dynamicSplitRoutes(packet, cSess) {
		t.Fatal("route beyond the configured limit was accepted")
	}
	select {
	case err := <-cSess.NetworkErrors:
		if err == nil {
			t.Fatal("route limit published healthy status")
		}
	default:
		t.Fatal("route limit was not published to health")
	}
}

func TestDynamicRouteExpiryPreservesSharedAddress(t *testing.T) {
	cSess := (&session.Session{}).NewConnSession(&http.Header{})
	shared := "203.0.113.10"
	cSess.DynamicSplitIncludeResolved.Store("expired.example", dynamicRouteLease{Addresses: []string{shared}, ExpiresAt: time.Now().Add(-time.Second)})
	cSess.DynamicSplitIncludeResolved.Store("active.example", dynamicRouteLease{Addresses: []string{shared}, ExpiresAt: time.Now().Add(time.Minute)})
	if !expireDynamicRoutes(cSess, time.Now()) {
		t.Fatal("expired lease was not removed")
	}
	routes := collectDynamicRoutes(cSess)
	if len(routes.Include) != 1 || routes.Include[0].String() != shared {
		t.Fatalf("shared active address was removed: %+v", routes.Include)
	}
}

func newTestTUNDevice() *testTUNDevice {
	return &testTUNDevice{events: make(chan wgtun.Event, 1)}
}

func (d *testTUNDevice) File() *os.File { return nil }
func (d *testTUNDevice) Read(_ [][]byte, _ []int, _ int) (int, error) {
	if d.readErr != nil {
		return 0, d.readErr
	}
	return 0, nil
}
func (d *testTUNDevice) Write(_ [][]byte, _ int) (int, error) {
	if d.writeErr != nil {
		return 0, d.writeErr
	}
	return 1, nil
}
func (d *testTUNDevice) MTU() (int, error)          { return 1399, nil }
func (d *testTUNDevice) Name() (string, error)      { return "test-tun", nil }
func (d *testTUNDevice) Events() <-chan wgtun.Event { return d.events }
func (d *testTUNDevice) BatchSize() int             { return 1 }
func (d *testTUNDevice) Close() error {
	d.closeOnce.Do(func() { close(d.events) })
	return nil
}

func waitSessionClosed(t *testing.T, cSess *session.ConnSession) {
	t.Helper()
	select {
	case <-cSess.CloseChan:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not close")
	}
}

func TestWatchTunEventsClosesSessionOnDown(t *testing.T) {
	sess := &session.Session{}
	cSess := sess.NewConnSession(&http.Header{})
	dev := newTestTUNDevice()

	done := make(chan struct{})
	go func() {
		watchTunEvents(dev, cSess)
		close(done)
	}()
	dev.events <- wgtun.EventDown

	waitSessionClosed(t, cSess)
	if info := cSess.CloseInfo(); info.Code != "tun_down" || info.Transport != "tun" {
		t.Fatalf("close info = %+v", info)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TUN event watcher did not stop")
	}
}

func TestTunReadErrorClosesSession(t *testing.T) {
	sess := &session.Session{}
	cSess := sess.NewConnSession(&http.Header{})
	dev := newTestTUNDevice()
	dev.readErr = errors.New("read failed")

	done := make(chan struct{})
	go func() {
		tunToPayloadOut(dev, cSess)
		close(done)
	}()

	waitSessionClosed(t, cSess)
	if info := cSess.CloseInfo(); info.Code != "tun_read_error" || info.Transport != "tun" {
		t.Fatalf("close info = %+v", info)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TUN read worker did not stop")
	}
}

func TestTunWriteErrorClosesSession(t *testing.T) {
	sess := &session.Session{}
	cSess := sess.NewConnSession(&http.Header{})
	dev := newTestTUNDevice()
	dev.writeErr = errors.New("write failed")
	cSess.PayloadIn <- &proto.Payload{Data: make([]byte, 20)}

	done := make(chan struct{})
	go func() {
		payloadInToTun(dev, cSess)
		close(done)
	}()

	waitSessionClosed(t, cSess)
	if info := cSess.CloseInfo(); info.Code != "tun_write_error" || info.Transport != "tun" {
		t.Fatalf("close info = %+v", info)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TUN write worker did not stop")
	}
}

func TestBuildOSNetConfig(t *testing.T) {
	getLocalInterface = func(context.Context) (osnet.LocalInterface, error) {
		return osnet.LocalInterface{Gateway: "192.168.1.1", InterfaceIndex: 36}, nil
	}
	t.Cleanup(func() { getLocalInterface = osnet.GetLocalInterface })
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
	wantExclude := []netip.Prefix{
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("222.66.117.109/32"),
	}
	if len(cfg.ExcludeRoutes) != len(wantExclude) {
		t.Fatalf("ExcludeRoutes = %v, want %v", cfg.ExcludeRoutes, wantExclude)
	}
	for i := range wantExclude {
		if cfg.ExcludeRoutes[i] != wantExclude[i] {
			t.Fatalf("ExcludeRoutes[%d] = %s, want %s", i, cfg.ExcludeRoutes[i], wantExclude[i])
		}
	}
	if len(cfg.DNSServers) != 2 || cfg.DNSServers[0].String() != "202.120.80.2" {
		t.Fatalf("DNSServers = %v", cfg.DNSServers)
	}
}

func TestApplyProfileOverridesLocalRoutesOverrideServerRoutes(t *testing.T) {
	profile := &auth.Profile{
		AcceptServerRoutes: true,
		ApplyDNS:           true,
		CustomInclude:      []string{"49.52.0.0/15"},
		CustomExclude:      []string{"202.120.0.0/16"},
	}
	cSess := &session.ConnSession{
		SplitInclude: []string{"49.52.0.0/15", "172.20.0.0/12"},
		SplitExclude: []string{"202.120.0.0/16"},
		DNS:          []string{"202.120.80.2"},
	}

	applyProfileOverridesWithProfile(cSess, profile)

	if !reflect.DeepEqual(cSess.SplitInclude, []string{"49.52.0.0/15", "172.16.0.0/12"}) {
		t.Fatalf("SplitInclude = %v", cSess.SplitInclude)
	}
	if !reflect.DeepEqual(cSess.SplitExclude, []string{"202.120.0.0/16"}) {
		t.Fatalf("SplitExclude = %v", cSess.SplitExclude)
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

func TestBuildOSNetConfigRejectsInvalidServerAddress(t *testing.T) {
	getLocalInterface = func(context.Context) (osnet.LocalInterface, error) {
		return osnet.LocalInterface{}, nil
	}
	t.Cleanup(func() { getLocalInterface = osnet.GetLocalInterface })
	cSess := &session.ConnSession{
		VPNAddress:    "172.20.144.185",
		VPNMask:       "255.255.255.255",
		ServerAddress: "vpn.example.test",
	}
	if _, err := buildOSNetConfig(cSess); err == nil {
		t.Fatal("buildOSNetConfig accepted an invalid VPN server address")
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
