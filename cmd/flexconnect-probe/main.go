package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	acAuth "flexconnect/internal/anyconnect/auth"
	acBase "flexconnect/internal/anyconnect/base"
	"flexconnect/internal/anyconnect/proto"
	"flexconnect/internal/anyconnect/session"
	"flexconnect/internal/anyconnect/utils"
	"flexconnect/internal/osnet"
	"github.com/pion/dtls/v3"
	"github.com/tailscale/wireguard-go/tun"
	"github.com/tailscale/wireguard-go/tun/netstack"
)

const (
	defaultEnvFile    = ".env"
	defaultObserveFor = 35 * time.Second
	maxEnvFileBytes   = 64 * 1024
	maxFrameBytes     = 64 * 1024
	defaultSpeedBytes = 4 * 1024 * 1024
	defaultSpeedLimit = 30 * time.Second
)

type credentials struct {
	Endpoint string
	Username string
	Password string
	Group    string
}

type cstpFrame struct {
	Type    byte
	Size    int
	Payload []byte
}

type speedtestConfig struct {
	URL      string
	MaxBytes int64
	Timeout  time.Duration
}

type speedtestResult struct {
	Bytes    int64
	Duration time.Duration
}

type speedtestOutcome struct {
	Result speedtestResult
	Err    error
}

type probeWriter struct {
	mu       sync.Mutex
	tlsConn  net.Conn
	dtlsConn *dtls.Conn
}

type trafficBridge struct {
	dev    tun.Device
	net    *netstack.Net
	writer *probeWriter

	mu              sync.Mutex
	sentBytes       int64
	receivedBytes   int64
	sentPackets     int64
	receivedPackets int64
	closeOnce       sync.Once
}

func main() {
	envFile := flag.String("env-file", defaultEnvFile, "dotenv file containing ENDPOINT, USERNAME, PASSWORD, and optional GROUP")
	endpointOverride := flag.String("endpoint", "", "optional VPN endpoint override; credentials still come from env-file")
	observeFor := flag.Duration("observe", defaultObserveFor, "how long to keep the TLS tunnel under observation")
	dpdInterval := flag.Duration("dpd-interval", 0, "TLS DPD interval; zero derives it from X-CSTP-DPD")
	noDTLS := flag.Bool("no-dtls", false, "do not open the secondary DTLS channel")
	debug := flag.Bool("debug", false, "enable protocol debug logging")
	localIP := flag.String("local-ip", "", "optional local IPv4 source address for the control connection")
	speedtestURL := flag.String("speedtest-url", "", "download URL to probe through the VPN user-space stack; empty disables traffic probing")
	speedtestBytes := flag.Int64("speedtest-bytes", defaultSpeedBytes, "maximum bytes to download during the traffic probe")
	speedtestTimeout := flag.Duration("speedtest-timeout", defaultSpeedLimit, "maximum duration of the traffic probe")
	flag.Parse()

	if *observeFor <= 0 {
		fatal("observe must be positive")
	}
	var speedtest *speedtestConfig
	if strings.TrimSpace(*speedtestURL) != "" {
		var err error
		speedtest, err = newSpeedtestConfig(*speedtestURL, *speedtestBytes, *speedtestTimeout)
		if err != nil {
			fatal("invalid speedtest configuration: %v", err)
		}
	}
	creds, err := loadCredentials(*envFile)
	if err != nil {
		fatal("load credentials: %v", err)
	}
	if strings.TrimSpace(*endpointOverride) != "" {
		creds.Endpoint = strings.TrimSpace(*endpointOverride)
	}
	if err := run(context.Background(), creds, *observeFor, *dpdInterval, !*noDTLS, *debug, *localIP, speedtest); err != nil {
		fatal("probe failed: %v", err)
	}
}

func run(ctx context.Context, creds credentials, observeFor, dpdInterval time.Duration, withDTLS, debug bool, localIP string, speedtest *speedtestConfig) error {
	acBase.Setup()
	if debug {
		acBase.SetLogLevel("Debug")
	} else {
		acBase.SetLogLevel("Info")
	}

	info, err := osnet.GetLocalInterface(ctx)
	if err != nil {
		return fmt.Errorf("inspect local interface: %w", err)
	}
	acBase.LocalInterface.Name = info.Name
	acBase.LocalInterface.Ip4 = info.IP4
	acBase.LocalInterface.Mac = info.MAC
	acBase.LocalInterface.Gateway = info.Gateway

	host, hostWithPort, groupAccess, err := endpointParts(creds.Endpoint)
	if err != nil {
		return err
	}
	acAuth.Prof.Host = host
	acAuth.Prof.HostWithPort = hostWithPort
	acAuth.Prof.GroupAccess = groupAccess
	acAuth.Prof.Username = creds.Username
	acAuth.Prof.Password = creds.Password
	acAuth.Prof.Group = creds.Group
	acAuth.Prof.SecretKey = ""

	if localIP != "" {
		if net.ParseIP(localIP).To4() == nil {
			return fmt.Errorf("invalid local IPv4 address %q", localIP)
		}
	}
	fmt.Printf("probe mode=cstp no_os_tun=true user_space_stack=%t endpoint=%s local_ip=%s auth_local_ip=%s\n", speedtest != nil, endpointForOutput(creds.Endpoint), info.IP4, safeHeader(localIP))
	var initAuthErr error
	if localIP == "" {
		initAuthErr = acAuth.InitAuth()
	} else {
		initAuthErr = acAuth.InitAuthWithLocalIP(localIP)
	}
	if initAuthErr != nil {
		return initAuthErr
	}
	fmt.Printf("auth connection local=%s remote=%s\n", safeAddr(acAuth.Conn.LocalAddr()), safeAddr(acAuth.Conn.RemoteAddr()))
	defer closeAuthConnection()
	if err := acAuth.PasswordAuth(); err != nil {
		return err
	}

	resp, err := negotiateCSTP()
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var dtlsConn *dtls.Conn
	if withDTLS && resp.Header.Get("X-DTLS-Port") != "" {
		dtlsConn, err = negotiateDTLS(resp.Header)
		if err != nil {
			return err
		}
		defer dtlsConn.Close()
	}
	interval := headerDuration(resp.Header.Get("X-CSTP-DPD"), dpdInterval)
	fmt.Printf("cstp status=%s vpn_ip=%s mtu=%s tls_dpd=%s tls_keepalive=%s dtls_enabled=%t dtls_port_present=%t dpd_interval=%s\n",
		resp.Status, safeHeader(resp.Header.Get("X-CSTP-Address")), safeHeader(resp.Header.Get("X-CSTP-MTU")),
		safeHeader(resp.Header.Get("X-CSTP-DPD")), safeHeader(resp.Header.Get("X-CSTP-Keepalive")),
		dtlsConn != nil, resp.Header.Get("X-DTLS-Port") != "", interval.Round(time.Millisecond))

	return observeCSTP(ctx, observeFor, interval, dtlsConn, speedtest, resp.Header)
}

func negotiateCSTP() (*http.Response, error) {
	masterSecret, err := utils.MakeMasterSecret()
	if err != nil {
		return nil, fmt.Errorf("create DTLS master secret: %w", err)
	}
	session.Sess.PreMasterSecret = masterSecret

	req, err := http.NewRequest(http.MethodConnect, acAuth.Prof.Scheme+acAuth.Prof.HostWithPort+"/CSCOSSLC/tunnel", nil)
	if err != nil {
		return nil, fmt.Errorf("create CSTP request: %w", err)
	}
	req.Header.Set("Cookie", "webvpn="+session.Sess.SessionToken)
	req.Header.Set("X-CSTP-VPNAddress-Type", "IPv4")
	req.Header.Set("X-CSTP-MTU", "1399")
	req.Header.Set("X-CSTP-Base-MTU", "1399")
	req.Header.Set("X-CSTP-Local-VPNAddress-IP4", acBase.LocalInterface.Ip4)
	req.Header.Set("X-DTLS-Master-Secret", fmt.Sprintf("%x", masterSecret))
	req.Header.Set("X-DTLS12-CipherSuite", "ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:AES128-GCM-SHA256")
	utils.SetCommonHeader(req)

	if err := req.Write(acAuth.Conn); err != nil {
		return nil, fmt.Errorf("write CSTP request: %w", err)
	}
	resp, err := http.ReadResponse(acAuth.BufR, req)
	if err != nil {
		return nil, fmt.Errorf("read CSTP response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("CSTP negotiation failed: %s", resp.Status)
	}
	return resp, nil
}

func observeCSTP(ctx context.Context, observeFor, dpdInterval time.Duration, dtlsConn *dtls.Conn, speedtest *speedtestConfig, headers http.Header) error {
	frames := make(chan cstpFrame, 16)
	errs := make(chan error, 1)
	go readFrames(acAuth.BufR, frames, errs)
	var dtlsFrames chan cstpFrame
	var dtlsErrs chan error
	if dtlsConn != nil {
		dtlsFrames = make(chan cstpFrame, 16)
		dtlsErrs = make(chan error, 1)
		go readDTLSFrames(dtlsConn, dtlsFrames, dtlsErrs)
	}
	writer := &probeWriter{tlsConn: acAuth.Conn, dtlsConn: dtlsConn}
	var bridge *trafficBridge
	var bridgeErrs chan error
	var speedtestDone <-chan speedtestOutcome
	if speedtest != nil {
		var err error
		bridge, err = newTrafficBridge(headers, writer)
		if err != nil {
			return err
		}
		defer bridge.Close()
		bridgeErrs = make(chan error, 1)
		go bridge.readOutbound(bridgeErrs)
		results := make(chan speedtestOutcome, 1)
		speedtestDone = results
		go func() {
			result, err := runSpeedtest(ctx, bridge.net, *speedtest)
			results <- speedtestOutcome{Result: result, Err: err}
		}()
		fmt.Printf("speedtest start=target:%s max_bytes=%d timeout=%s transport=%s\n",
			speedtestHost(speedtest.URL), speedtest.MaxBytes, speedtest.Timeout, transportName(dtlsConn))
	}

	ticker := time.NewTicker(dpdInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(observeFor)
	defer deadline.Stop()
	started := time.Now()
	speedtestFinished := speedtest == nil
	for {
		select {
		case frame := <-frames:
			if bridge != nil {
				if err := bridge.handleInbound(frame); err != nil {
					return fmt.Errorf("inject CSTP frame into user-space stack: %w", err)
				}
			}
			if frame.Type != 0x00 || bridge == nil {
				fmt.Printf("frame elapsed=%s type=0x%02x payload=%d\n", time.Since(started).Round(time.Millisecond), frame.Type, frame.Size)
			}
			if frame.Type == 0x05 || frame.Type == 0x09 {
				return fmt.Errorf("server sent CSTP termination frame type=0x%02x", frame.Type)
			}
		case frame := <-dtlsFrames:
			if bridge != nil {
				if err := bridge.handleInbound(frame); err != nil {
					return fmt.Errorf("inject DTLS frame into user-space stack: %w", err)
				}
			}
			if frame.Type != 0x00 || bridge == nil {
				fmt.Printf("dtls_frame elapsed=%s type=0x%02x payload=%d\n", time.Since(started).Round(time.Millisecond), frame.Type, frame.Size)
			}
			if frame.Type == 0x05 || frame.Type == 0x09 {
				return fmt.Errorf("server sent DTLS termination frame type=0x%02x", frame.Type)
			}
		case err := <-errs:
			return fmt.Errorf("CSTP read failed after %s: %w", time.Since(started).Round(time.Millisecond), err)
		case err := <-dtlsErrs:
			return fmt.Errorf("DTLS read failed after %s: %w", time.Since(started).Round(time.Millisecond), err)
		case err := <-bridgeErrs:
			return fmt.Errorf("user-space outbound traffic failed after %s: %w", time.Since(started).Round(time.Millisecond), err)
		case outcome := <-speedtestDone:
			if outcome.Err != nil {
				return fmt.Errorf("speedtest failed after %s: %w", time.Since(started).Round(time.Millisecond), outcome.Err)
			}
			speedtestFinished = true
			stats := bridge.stats()
			bitsPerSecond := float64(outcome.Result.Bytes*8) / outcome.Result.Duration.Seconds()
			fmt.Printf("speedtest result=ok bytes=%d duration=%s mibps=%.2f transport=%s outbound_frame_bytes=%d inbound_frame_bytes=%d outbound_packets=%d inbound_packets=%d\n",
				outcome.Result.Bytes, outcome.Result.Duration.Round(time.Millisecond), bitsPerSecond/(1024*1024), transportName(dtlsConn),
				stats.sentBytes, stats.receivedBytes, stats.sentPackets, stats.receivedPackets)
		case <-ticker.C:
			if err := writer.sendControl(0x03); err != nil {
				return fmt.Errorf("send TLS DPD after %s: %w", time.Since(started).Round(time.Millisecond), err)
			}
			fmt.Printf("dpd elapsed=%s dtls=%t\n", time.Since(started).Round(time.Millisecond), dtlsConn != nil)
		case <-deadline.C:
			if !speedtestFinished {
				return fmt.Errorf("speedtest did not finish within observation window")
			}
			fmt.Printf("probe result=stable_tls_observation duration=%s dtls=%t\n", time.Since(started).Round(time.Millisecond), dtlsConn != nil)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func negotiateDTLS(headers http.Header) (*dtls.Conn, error) {
	port, err := strconv.Atoi(strings.TrimSpace(headers.Get("X-DTLS-Port")))
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid X-DTLS-Port")
	}
	remote, ok := acAuth.Conn.RemoteAddr().(*net.TCPAddr)
	if !ok || remote.IP == nil {
		return nil, fmt.Errorf("TLS peer address is unavailable for DTLS")
	}
	idValue := headers.Get("X-DTLS-Session-ID")
	if idValue == "" {
		idValue = headers.Get("X-DTLS-App-ID")
	}
	id, err := hex.DecodeString(idValue)
	if err != nil {
		return nil, fmt.Errorf("invalid DTLS session identifier")
	}
	config := &dtls.Config{
		InsecureSkipVerify:   true,
		ExtendedMasterSecret: dtls.DisableExtendedMasterSecret,
		CipherSuites:         dtlsCipherSuites(headers.Get("X-DTLS12-CipherSuite")),
		SessionStore: &probeDTLSSessionStore{value: dtls.Session{
			ID: id, Secret: session.Sess.PreMasterSecret,
		}},
	}
	conn, err := dtls.Dial("udp4", &net.UDPAddr{IP: remote.IP, Port: port}, config)
	if err != nil {
		return nil, fmt.Errorf("DTLS dial: %w", err)
	}
	handshakeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := conn.HandshakeContext(handshakeCtx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("DTLS handshake: %w", err)
	}
	fmt.Printf("dtls handshake=ok peer=%s:%d\n", remote.IP, port)
	return conn, nil
}

func dtlsCipherSuites(raw string) []dtls.CipherSuiteID {
	var suites []dtls.CipherSuiteID
	for _, name := range strings.Split(raw, ":") {
		switch strings.TrimSpace(name) {
		case "ECDHE-ECDSA-AES128-GCM-SHA256":
			suites = append(suites, dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256)
		case "ECDHE-RSA-AES128-GCM-SHA256":
			suites = append(suites, dtls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256)
		case "ECDHE-ECDSA-AES256-GCM-SHA384":
			suites = append(suites, dtls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384)
		case "ECDHE-RSA-AES256-GCM-SHA384":
			suites = append(suites, dtls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		}
	}
	if len(suites) == 0 {
		return []dtls.CipherSuiteID{dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}
	}
	return suites
}

type probeDTLSSessionStore struct {
	value dtls.Session
}

func (s *probeDTLSSessionStore) Set([]byte, dtls.Session) error { return nil }
func (s *probeDTLSSessionStore) Get([]byte) (dtls.Session, error) {
	return s.value, nil
}
func (s *probeDTLSSessionStore) Del([]byte) error { return nil }

func (w *probeWriter) sendControl(frameType byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.tlsConn != nil {
		frame := append([]byte(nil), proto.Header...)
		frame[6] = frameType
		if _, err := w.tlsConn.Write(frame); err != nil {
			return fmt.Errorf("TLS control frame: %w", err)
		}
	}
	if w.dtlsConn != nil {
		if _, err := w.dtlsConn.Write([]byte{frameType}); err != nil {
			return fmt.Errorf("DTLS control frame: %w", err)
		}
	}
	return nil
}

func (w *probeWriter) sendData(packet []byte) error {
	if len(packet) == 0 {
		return errors.New("empty IPv4 packet")
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.dtlsConn != nil {
		frame := make([]byte, len(packet)+1)
		copy(frame[1:], packet)
		if _, err := w.dtlsConn.Write(frame); err != nil {
			return fmt.Errorf("DTLS data frame: %w", err)
		}
		return nil
	}
	frame := make([]byte, len(packet)+len(proto.Header))
	copy(frame, proto.Header)
	frame[4] = byte(len(packet) >> 8)
	frame[5] = byte(len(packet))
	copy(frame[len(proto.Header):], packet)
	if _, err := w.tlsConn.Write(frame); err != nil {
		return fmt.Errorf("TLS data frame: %w", err)
	}
	return nil
}

func newTrafficBridge(headers http.Header, writer *probeWriter) (*trafficBridge, error) {
	local, err := netip.ParseAddr(strings.TrimSpace(headers.Get("X-CSTP-Address")))
	if err != nil || !local.Is4() {
		return nil, fmt.Errorf("invalid VPN IPv4 address %q", headers.Get("X-CSTP-Address"))
	}
	dns, err := parseProbeDNS(headers.Values("X-CSTP-DNS"))
	if err != nil {
		return nil, err
	}
	mtu, err := strconv.Atoi(strings.TrimSpace(headers.Get("X-CSTP-MTU")))
	if err != nil || mtu < 576 || mtu > 65535 {
		return nil, fmt.Errorf("invalid VPN MTU %q", headers.Get("X-CSTP-MTU"))
	}
	dev, tnet, err := netstack.CreateNetTUN([]netip.Addr{local}, dns, mtu)
	if err != nil {
		return nil, fmt.Errorf("create user-space network stack: %w", err)
	}
	return &trafficBridge{dev: dev, net: tnet, writer: writer}, nil
}

func parseProbeDNS(values []string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			addr, err := netip.ParseAddr(item)
			if err != nil || !addr.Is4() {
				return nil, fmt.Errorf("invalid VPN DNS address %q", item)
			}
			out = append(out, addr)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("CSTP response did not provide an IPv4 DNS server")
	}
	return out, nil
}

func (b *trafficBridge) readOutbound(errs chan<- error) {
	mtu, err := b.dev.MTU()
	if err != nil {
		errs <- err
		return
	}
	buffer := make([]byte, mtu)
	for {
		sizes := []int{0}
		if _, err := b.dev.Read([][]byte{buffer}, sizes, 0); err != nil {
			errs <- err
			return
		}
		if sizes[0] <= 0 || sizes[0] > len(buffer) {
			errs <- fmt.Errorf("user-space stack returned invalid packet size %d", sizes[0])
			return
		}
		packet := append([]byte(nil), buffer[:sizes[0]]...)
		if err := b.writer.sendData(packet); err != nil {
			errs <- err
			return
		}
		b.mu.Lock()
		b.sentBytes += int64(len(packet))
		b.sentPackets++
		b.mu.Unlock()
	}
}

func (b *trafficBridge) handleInbound(frame cstpFrame) error {
	if frame.Type != 0x00 || len(frame.Payload) == 0 {
		return nil
	}
	if _, err := b.dev.Write([][]byte{frame.Payload}, 0); err != nil {
		return err
	}
	b.mu.Lock()
	b.receivedBytes += int64(len(frame.Payload))
	b.receivedPackets++
	b.mu.Unlock()
	return nil
}

func (b *trafficBridge) stats() trafficStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return trafficStats{
		sentBytes:       b.sentBytes,
		receivedBytes:   b.receivedBytes,
		sentPackets:     b.sentPackets,
		receivedPackets: b.receivedPackets,
	}
}

type trafficStats struct {
	sentBytes       int64
	receivedBytes   int64
	sentPackets     int64
	receivedPackets int64
}

func (b *trafficBridge) Close() {
	if b == nil {
		return
	}
	b.closeOnce.Do(func() {
		_ = b.dev.Close()
	})
}

func runSpeedtest(parent context.Context, tnet *netstack.Net, config speedtestConfig) (speedtestResult, error) {
	ctx, cancel := context.WithTimeout(parent, config.Timeout)
	defer cancel()
	transport := &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: false,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return tnet.DialContext(ctx, network, address)
		},
	}
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, config.URL, nil)
	if err != nil {
		return speedtestResult{}, err
	}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return speedtestResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return speedtestResult{}, fmt.Errorf("speedtest returned %s", resp.Status)
	}
	bytesRead, err := io.CopyBuffer(io.Discard, io.LimitReader(resp.Body, config.MaxBytes), make([]byte, 32*1024))
	if err != nil {
		return speedtestResult{}, err
	}
	duration := time.Since(started)
	if bytesRead == 0 {
		return speedtestResult{}, errors.New("speedtest returned no payload")
	}
	return speedtestResult{Bytes: bytesRead, Duration: duration}, nil
}

func newSpeedtestConfig(rawURL string, maxBytes int64, timeout time.Duration) (*speedtestConfig, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("speedtest URL must be an absolute http(s) URL")
	}
	if parsed.User != nil {
		return nil, errors.New("speedtest URL must not contain user info")
	}
	if maxBytes <= 0 {
		return nil, errors.New("speedtest-bytes must be positive")
	}
	if timeout <= 0 {
		return nil, errors.New("speedtest-timeout must be positive")
	}
	return &speedtestConfig{URL: parsed.String(), MaxBytes: maxBytes, Timeout: timeout}, nil
}

func speedtestHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return "invalid"
}

func transportName(dtlsConn *dtls.Conn) string {
	if dtlsConn != nil {
		return "dtls"
	}
	return "tls"
}

func readDTLSFrames(conn *dtls.Conn, frames chan<- cstpFrame, errs chan<- error) {
	buffer := make([]byte, maxFrameBytes)
	for {
		size, err := conn.Read(buffer)
		if err != nil {
			errs <- err
			return
		}
		if size == 0 {
			continue
		}
		payload := append([]byte(nil), buffer[1:size]...)
		frames <- cstpFrame{Type: buffer[0], Size: len(payload), Payload: payload}
	}
}

func readFrames(reader io.Reader, frames chan<- cstpFrame, errs chan<- error) {
	for {
		var header [8]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			errs <- err
			return
		}
		if !bytes.Equal(header[:4], proto.Header[:4]) || header[7] != 0 {
			errs <- fmt.Errorf("invalid CSTP header %x", header)
			return
		}
		payloadSize := int(binary.BigEndian.Uint16(header[4:6]))
		if payloadSize > maxFrameBytes {
			errs <- fmt.Errorf("CSTP payload exceeds %d bytes: %d", maxFrameBytes, payloadSize)
			return
		}
		payload := make([]byte, payloadSize)
		if _, err := io.ReadFull(reader, payload); err != nil {
			errs <- err
			return
		}
		frames <- cstpFrame{Type: header[6], Size: payloadSize, Payload: payload}
	}
}

func closeAuthConnection() {
	if acAuth.Conn != nil {
		_ = acAuth.Conn.Close()
		acAuth.Conn = nil
	}
}

func headerDuration(raw string, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err == nil && value > 0 {
		interval := time.Duration(value-5) * time.Second
		if interval >= 10*time.Second {
			return interval
		}
	}
	return 10 * time.Second
}

func endpointParts(raw string) (host, hostWithPort, groupAccess string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", errors.New("ENDPOINT is empty")
	}
	parseRaw := raw
	if !strings.Contains(parseRaw, "://") {
		parseRaw = "https://" + parseRaw
	}
	parsed, err := url.Parse(parseRaw)
	if err != nil || parsed.Host == "" {
		if err == nil {
			err = errors.New("missing host")
		}
		return "", "", "", fmt.Errorf("invalid ENDPOINT: %w", err)
	}
	host = parsed.Host
	if parsed.Port() == "" {
		hostWithPort = net.JoinHostPort(parsed.Hostname(), "443")
	} else {
		hostWithPort = parsed.Host
	}
	parsed.Scheme = "https"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	groupAccess = strings.TrimRight(parsed.String(), "/")
	return host, hostWithPort, groupAccess, nil
}

func loadCredentials(path string) (credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return credentials{}, err
	}
	if len(data) > maxEnvFileBytes {
		return credentials{}, fmt.Errorf("dotenv file exceeds %d bytes", maxEnvFileBytes)
	}
	values, err := parseDotenv(string(data))
	if err != nil {
		return credentials{}, err
	}
	creds := credentials{Endpoint: values["ENDPOINT"], Username: values["USERNAME"], Password: values["PASSWORD"], Group: values["GROUP"]}
	var missing []string
	if strings.TrimSpace(creds.Endpoint) == "" {
		missing = append(missing, "ENDPOINT")
	}
	if strings.TrimSpace(creds.Username) == "" {
		missing = append(missing, "USERNAME")
	}
	if creds.Password == "" {
		missing = append(missing, "PASSWORD")
	}
	if len(missing) != 0 {
		return credentials{}, fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	return creds, nil
}

func parseDotenv(raw string) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(raw, "\ufeff")))
	line := 0
	for scanner.Scan() {
		line++
		value := strings.TrimSpace(scanner.Text())
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		value = strings.TrimPrefix(value, "export ")
		key, rawValue, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || !validEnvKey(key) {
			return nil, fmt.Errorf("invalid dotenv assignment at line %d", line)
		}
		parsed, err := parseDotenvValue(strings.TrimSpace(rawValue))
		if err != nil {
			return nil, fmt.Errorf("invalid dotenv value at line %d: %w", line, err)
		}
		values[key] = parsed
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseDotenvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", errors.New("unterminated single quote")
		}
		return value[1 : len(value)-1], nil
	}
	if value[0] == '"' {
		if len(value) < 2 || value[len(value)-1] != '"' {
			return "", errors.New("unterminated double quote")
		}
		return strings.ReplaceAll(value[1:len(value)-1], `\n`, "\n"), nil
	}
	if index := strings.Index(value, " #"); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	return value, nil
}

func validEnvKey(value string) bool {
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return value != ""
}

func endpointForOutput(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://"))
}

func safeHeader(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
}

func safeAddr(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return safeHeader(addr.String())
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
