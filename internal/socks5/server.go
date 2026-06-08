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
)

type Server struct {
	listener net.Listener
	dialer   net.Dialer
	addr     string
	wg       sync.WaitGroup
}

func Listen(addr string) (*Server, error) {
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
		dialer: net.Dialer{
			Timeout: 30 * time.Second,
		},
	}
	server.wg.Add(1)
	go server.serve()
	return server, nil
}

func (s *Server) Addr() string {
	if s == nil {
		return ""
	}
	return s.addr
}

func (s *Server) Close() error {
	if s == nil || s.listener == nil {
		return nil
	}
	err := s.listener.Close()
	s.wg.Wait()
	return err
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			_ = s.handleConn(conn)
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

	upstream, err := s.dialer.DialContext(context.Background(), "tcp", target)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return err
	}
	defer upstream.Close()
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}

	bindAddr := upstream.LocalAddr().(*net.TCPAddr)
	if err := writeReply(conn, bindAddr); err != nil {
		return err
	}
	return proxyBidirectional(conn, upstream)
}

func readTarget(r io.Reader, atyp byte) (string, error) {
	host, err := readHost(r, atyp)
	if err != nil {
		return "", err
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(r, portBuf[:]); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf[:])
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
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
