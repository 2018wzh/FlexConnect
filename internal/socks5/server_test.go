package socks5

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeTunnelDialer struct {
	mu      sync.Mutex
	lookups []string
	dials   []string
	lookup  map[string][]string
	err     error
	peers   []net.Conn
}

func (d *fakeTunnelDialer) LookupContextHost(_ context.Context, host string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lookups = append(d.lookups, host)
	if d.err != nil {
		return nil, d.err
	}
	if addrs, ok := d.lookup[host]; ok {
		return append([]string(nil), addrs...), nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (d *fakeTunnelDialer) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dials = append(d.dials, network+" "+address)
	if d.err != nil {
		return nil, d.err
	}
	left, right := net.Pipe()
	d.peers = append(d.peers, right)
	return left, nil
}

func (d *fakeTunnelDialer) closePeers() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, peer := range d.peers {
		_ = peer.Close()
	}
	d.peers = nil
}

func TestListenRequiresTunnelDialer(t *testing.T) {
	server, err := Listen("127.0.0.1:0", nil)
	if err == nil {
		_ = server.Close()
		t.Fatal("Listen succeeded with nil tunnel dialer")
	}
}

func TestDomainTargetsResolveThroughTunnelBeforeDial(t *testing.T) {
	dialer := &fakeTunnelDialer{lookup: map[string][]string{"example.test": {"10.64.0.7"}}}
	client, server := net.Pipe()
	defer client.Close()
	defer dialer.closePeers()
	done := make(chan error, 1)
	go func() {
		done <- (&Server{dialer: dialer}).handleConn(server)
	}()

	writeGreetingAndConnect(t, client, 0x03, []byte("example.test"), 443)
	reply := readReply(t, client)
	if reply[1] != 0x00 {
		t.Fatalf("reply code = %#x, want success", reply[1])
	}
	client.Close()
	dialer.closePeers()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SOCKS handler did not exit")
	}

	if got := dialer.lookups; len(got) != 1 || got[0] != "example.test" {
		t.Fatalf("lookups = %v", got)
	}
	if got := dialer.dials; len(got) != 1 || got[0] != "tcp4 10.64.0.7:443" {
		t.Fatalf("dials = %v", got)
	}
}

func TestIPTargetsDialTunnelWithoutResolver(t *testing.T) {
	dialer := &fakeTunnelDialer{lookup: map[string][]string{}}
	client, server := net.Pipe()
	defer client.Close()
	defer dialer.closePeers()
	done := make(chan error, 1)
	go func() {
		done <- (&Server{dialer: dialer}).handleConn(server)
	}()

	writeGreetingAndConnect(t, client, 0x01, []byte{10, 64, 0, 8}, 8443)
	reply := readReply(t, client)
	if reply[1] != 0x00 {
		t.Fatalf("reply code = %#x, want success", reply[1])
	}
	client.Close()
	dialer.closePeers()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SOCKS handler did not exit")
	}

	if len(dialer.lookups) != 0 {
		t.Fatalf("lookups = %v, want none", dialer.lookups)
	}
	if got := dialer.dials; len(got) != 1 || got[0] != "tcp4 10.64.0.8:8443" {
		t.Fatalf("dials = %v", got)
	}
}

func TestDomainLookupFailureDoesNotFallbackToSystemDialer(t *testing.T) {
	dialer := &fakeTunnelDialer{err: errors.New("vpn dns unavailable")}
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		done <- (&Server{dialer: dialer}).handleConn(server)
	}()

	writeGreetingAndConnect(t, client, 0x03, []byte("example.test"), 443)
	reply := readReply(t, client)
	if reply[1] != 0x04 {
		t.Fatalf("reply code = %#x, want host unreachable", reply[1])
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SOCKS handler did not exit")
	}
	if len(dialer.dials) != 0 {
		t.Fatalf("dials = %v, want none", dialer.dials)
	}
}

func writeGreetingAndConnect(t *testing.T, conn net.Conn, atyp byte, host []byte, port int) {
	t.Helper()
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		t.Fatalf("read method: %v", err)
	}
	if method != [2]byte{0x05, 0x00} {
		t.Fatalf("method = %v", method)
	}
	req := []byte{0x05, 0x01, 0x00, atyp}
	if atyp == 0x03 {
		req = append(req, byte(len(host)))
	}
	req = append(req, host...)
	portValue := uint16(port)
	req = append(req, byte(portValue>>8), byte(portValue))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write connect %s: %v", strconv.Itoa(port), err)
	}
}

func readReply(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	return reply
}
