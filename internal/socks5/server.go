package socks5

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"flexconnect/internal/logging"
)

var socks5Log = logging.WithComponent("socks5")

type TunnelDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
	LookupContextHost(context.Context, string) ([]string, error)
}

type Server struct {
	listener  net.Listener
	dialer    TunnelDialer
	addr      string
	wg        sync.WaitGroup
	mu        sync.Mutex
	conns     map[net.Conn]struct{}
	closed    bool
	closeErr  error
	closeOnce sync.Once
	errors    chan error
}

func Listen(addr string, dialer TunnelDialer) (*Server, error) {
	if dialer == nil {
		return nil, errors.New("SOCKS5 requires a VPN tunnel dialer")
	}
	if addr == "" {
		addr = "127.0.0.1:1080"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	server := &Server{
		listener: ln,
		addr:     ln.Addr().String(),
		dialer:   dialer,
		conns:    make(map[net.Conn]struct{}),
		errors:   make(chan error, 1),
	}
	server.wg.Add(1)
	go server.serve()
	return server, nil
}

func (s *Server) Errors() <-chan error { return s.errors }

func (s *Server) Addr() string {
	if s == nil {
		return ""
	}
	return s.addr
}

func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.CloseContext(ctx)
}

func (s *Server) CloseContext(ctx context.Context) error {
	if s == nil || s.listener == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.closeErr = s.listener.Close()
		if errors.Is(s.closeErr, net.ErrClosed) {
			s.closeErr = nil
		}
		for conn := range s.conns {
			if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) && s.closeErr == nil {
				s.closeErr = err
			}
		}
		s.mu.Unlock()
	})
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.mu.Lock()
		err := s.closeErr
		s.mu.Unlock()
		return err
	case <-ctx.Done():
		return fmt.Errorf("close SOCKS5 server: %w", ctx.Err())
	}
}

func (s *Server) serve() {
	defer s.wg.Done()
	defer close(s.errors)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			socks5Log.Printf("accept failed err=%q", err.Error())
			select {
			case s.errors <- fmt.Errorf("SOCKS5 accept failed: %w", err):
			default:
			}
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
				_ = conn.Close()
			}()
			if err := s.handleConn(conn); err != nil && !errors.Is(err, net.ErrClosed) {
				socks5Log.Printf("client session failed remote=%s err=%q", conn.RemoteAddr(), err.Error())
			}
		}()
	}
}

func (s *Server) handleConn(conn net.Conn) error {
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return err
	}
	if hdr[0] != 0x05 {
		return fmt.Errorf("unsupported SOCKS version %d", hdr[0])
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	noAuth := false
	for _, method := range methods {
		if method == 0x00 {
			noAuth = true
			break
		}
	}
	if !noAuth {
		_, _ = conn.Write([]byte{0x05, 0xff})
		return errors.New("SOCKS5 client did not offer no-authentication method")
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return err
	}

	var reqHdr [4]byte
	if _, err := io.ReadFull(conn, reqHdr[:]); err != nil {
		return err
	}
	if reqHdr[0] != 0x05 {
		return fmt.Errorf("unsupported request version %d", reqHdr[0])
	}
	if reqHdr[1] != 0x01 {
		_, _ = conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return fmt.Errorf("unsupported command %d", reqHdr[1])
	}
	target, err := readTarget(conn, reqHdr[3])
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	upstream, err := s.dialTarget(ctx, target)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, replyCodeForDialError(err), 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return err
	}
	defer upstream.Close()
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}

	bindAddr, ok := upstream.LocalAddr().(*net.TCPAddr)
	if !ok {
		bindAddr = &net.TCPAddr{IP: net.IPv4zero, Port: 0}
	}
	if err := writeReply(conn, bindAddr); err != nil {
		return err
	}
	return proxyBidirectional(conn, upstream)
}

type targetAddr struct {
	host string
	port string
	atyp byte
}

func readTarget(r io.Reader, atyp byte) (targetAddr, error) {
	host, err := readHost(r, atyp)
	if err != nil {
		return targetAddr{}, err
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(r, portBuf[:]); err != nil {
		return targetAddr{}, err
	}
	port := binary.BigEndian.Uint16(portBuf[:])
	return targetAddr{host: host, port: strconv.Itoa(int(port)), atyp: atyp}, nil
}

var (
	errNoTunnelAddress      = errors.New("no VPN DNS address")
	errUnsupportedIPv6Proxy = errors.New("SOCKS5 VPN proxy supports IPv4 TCP targets only")
)

func (s *Server) dialTarget(ctx context.Context, target targetAddr) (net.Conn, error) {
	if s.dialer == nil {
		return nil, errors.New("missing VPN tunnel dialer")
	}
	if ip := net.ParseIP(target.host); ip != nil {
		v4 := ip.To4()
		if v4 == nil {
			return nil, errUnsupportedIPv6Proxy
		}
		return s.dialer.DialContext(ctx, "tcp4", net.JoinHostPort(v4.String(), target.port))
	}
	addrs, err := s.dialer.LookupContextHost(ctx, target.host)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoTunnelAddress, err)
	}
	var firstErr error
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil || ip.To4() == nil {
			continue
		}
		conn, err := s.dialer.DialContext(ctx, "tcp4", net.JoinHostPort(ip.To4().String(), target.port))
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, errNoTunnelAddress
}

func replyCodeForDialError(err error) byte {
	if errors.Is(err, errUnsupportedIPv6Proxy) {
		return 0x08
	}
	if errors.Is(err, errNoTunnelAddress) {
		return 0x04
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return 0x04
	}
	return 0x05
}

func readHost(r io.Reader, atyp byte) (string, error) {
	switch atyp {
	case 0x01:
		var ip [4]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return "", err
		}
		return net.IP(ip[:]).String(), nil
	case 0x03:
		var length [1]byte
		if _, err := io.ReadFull(r, length[:]); err != nil {
			return "", err
		}
		data := make([]byte, length[0])
		if _, err := io.ReadFull(r, data); err != nil {
			return "", err
		}
		return string(data), nil
	case 0x04:
		var ip [16]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return "", err
		}
		return net.IP(ip[:]).String(), nil
	default:
		return "", fmt.Errorf("unsupported address type %d", atyp)
	}
}

func writeReply(w io.Writer, addr *net.TCPAddr) error {
	ip := addr.IP
	if v4 := ip.To4(); v4 != nil {
		reply := []byte{0x05, 0x00, 0x00, 0x01}
		reply = append(reply, v4...)
		reply = binary.BigEndian.AppendUint16(reply, uint16(addr.Port))
		_, err := w.Write(reply)
		return err
	}
	reply := []byte{0x05, 0x00, 0x00, 0x04}
	reply = append(reply, ip.To16()...)
	reply = binary.BigEndian.AppendUint16(reply, uint16(addr.Port))
	_, err := w.Write(reply)
	return err
}

func proxyBidirectional(left, right net.Conn) error {
	errCh := make(chan error, 2)
	copyFn := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		errCh <- err
	}
	go copyFn(left, right)
	go copyFn(right, left)
	err1 := <-errCh
	err2 := <-errCh
	if err1 != nil && !errors.Is(err1, net.ErrClosed) {
		return err1
	}
	if err2 != nil && !errors.Is(err2, net.ErrClosed) {
		return err2
	}
	return nil
}
