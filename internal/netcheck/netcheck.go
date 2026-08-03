package netcheck

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
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
	DefaultEnvFile      = ".env"
	DefaultObserveFor   = 35 * time.Second
	maxEnvFileBytes     = 64 * 1024
	maxFrameBytes       = 64 * 1024
	DefaultSpeedBytes   = 4 * 1024 * 1024
	DefaultSpeedLimit   = 30 * time.Second
	DefaultSpeedtestURL = "https://speed.cloudflare.com/__down?bytes=4194304"
	defaultMTU          = 1399
)

type Credentials struct {
	Endpoint string
	Username string
	Password string
	Group    string
}

type Config struct {
	Credentials Credentials
	ObserveFor  time.Duration
	DPDInterval time.Duration
	WithDTLS    bool
	Debug       bool
	LocalIP     string
	Speedtest   *SpeedtestConfig
	MTU         int
}

type Result struct {
	Status              string           `json:"status"`
	Mode                string           `json:"mode"`
	UserSpaceStack      bool             `json:"user_space_stack"`
	Endpoint            string           `json:"endpoint"`
	LocalInterface      string           `json:"local_interface"`
	LocalIPv4           string           `json:"local_ipv4"`
	Gateway             string           `json:"gateway"`
	RequestedLocalIP    string           `json:"requested_local_ip,omitempty"`
	AuthLocalAddress    string           `json:"auth_local_address"`
	AuthRemoteAddress   string           `json:"auth_remote_address"`
	CSTPStatus          string           `json:"cstp_status"`
	VPNAddress          string           `json:"vpn_address"`
	MTU                 int              `json:"mtu"`
	TLSDPD              string           `json:"tls_dpd"`
	TLSKeepalive        string           `json:"tls_keepalive"`
	DTLSEnabled         bool             `json:"dtls_enabled"`
	DTLSPort            string           `json:"dtls_port,omitempty"`
	DTLSPeer            string           `json:"dtls_peer,omitempty"`
	Transport           string           `json:"transport"`
	DPDInterval         time.Duration    `json:"dpd_interval"`
	ObservationDuration time.Duration    `json:"observation_duration"`
	TLSFrames           uint64           `json:"tls_frames"`
	DTLSFrames          uint64           `json:"dtls_frames"`
	DPDSent             uint64           `json:"dpd_sent"`
	Speedtest           *SpeedtestResult `json:"speedtest,omitempty"`
}

type cstpFrame struct {
	Type    byte
	Size    int
	Payload []byte
}

type SpeedtestConfig struct {
	URL      string        `json:"url"`
	MaxBytes int64         `json:"max_bytes"`
	Timeout  time.Duration `json:"timeout"`
}

type SpeedtestResult struct {
	TargetHost         string        `json:"target_host"`
	Bytes              int64         `json:"bytes"`
	Duration           time.Duration `json:"duration"`
	MiBPS              float64       `json:"mibps"`
	Transport          string        `json:"transport"`
	OutboundFrameBytes int64         `json:"outbound_frame_bytes"`
	InboundFrameBytes  int64         `json:"inbound_frame_bytes"`
	OutboundPackets    int64         `json:"outbound_packets"`
	InboundPackets     int64         `json:"inbound_packets"`
}

type speedtestOutcome struct {
	Result SpeedtestResult
	Err    error
}

type observationResult struct {
	Duration   time.Duration
	TLSFrames  uint64
	DTLSFrames uint64
	DPDSent    uint64
	Speedtest  *SpeedtestResult
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

func Run(ctx context.Context, config Config) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("nil netcheck context")
	}
	if config.ObserveFor <= 0 {
		return Result{}, errors.New("netcheck observation duration must be positive")
	}
	if config.DPDInterval < 0 {
		return Result{}, errors.New("netcheck DPD interval must not be negative")
	}
	if config.MTU <= 0 {
		config.MTU = defaultMTU
	}
	if config.MTU < 576 || config.MTU > 65535 {
		return Result{}, fmt.Errorf("invalid netcheck MTU %d", config.MTU)
	}
	if strings.TrimSpace(config.Credentials.Endpoint) == "" {
		return Result{}, errors.New("netcheck endpoint is empty")
	}
	acBase.Setup()
	if config.Debug {
		acBase.SetLogLevel("Debug")
	} else {
		acBase.SetLogLevel("Info")
	}

	info, err := osnet.GetLocalInterface(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("inspect local interface: %w", err)
	}
	host, hostWithPort, groupAccess, err := endpointParts(config.Credentials.Endpoint)
	if err != nil {
		return Result{}, err
	}
	profile := acAuth.Profile{
		Host: host, HostWithPort: hostWithPort, GroupAccess: groupAccess,
		Username: config.Credentials.Username, Password: config.Credentials.Password,
		Group: config.Credentials.Group, SecretKey: "", Scheme: "https://", MTU: config.MTU,
	}
	client := acAuth.NewClient(profile, acBase.Interface{Name: info.Name, Ip4: info.IP4, Mac: info.MAC, Gateway: info.Gateway})
	if config.LocalIP != "" && net.ParseIP(strings.TrimSpace(config.LocalIP)).To4() == nil {
		return Result{}, fmt.Errorf("invalid local IPv4 address %q", config.LocalIP)
	}
	var localAddr net.Addr
	if config.LocalIP != "" {
		localAddr = &net.TCPAddr{IP: net.ParseIP(strings.TrimSpace(config.LocalIP)).To4()}
	}
	result := Result{
		Status:           "running",
		Mode:             "CSTP",
		UserSpaceStack:   config.Speedtest != nil,
		Endpoint:         endpointForOutput(config.Credentials.Endpoint),
		LocalInterface:   info.Name,
		LocalIPv4:        info.IP4,
		Gateway:          info.Gateway,
		RequestedLocalIP: safeHeader(config.LocalIP),
	}
	sess := &session.Session{}
	if err := client.InitAuth(localAddr); err != nil {
		return result, err
	}
	defer func() { _ = client.Close() }()
	if client.Conn == nil {
		return result, errors.New("authentication completed without a connection")
	}
	result.AuthLocalAddress = safeAddr(client.Conn.LocalAddr())
	result.AuthRemoteAddress = safeAddr(client.Conn.RemoteAddr())
	if err := client.PasswordAuth(sess); err != nil {
		return result, err
	}
	resp, err := negotiateCSTP(client, sess, info.IP4, config.MTU)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	result.CSTPStatus = resp.Status
	result.VPNAddress = safeHeader(resp.Header.Get("X-CSTP-Address"))
	result.TLSDPD = safeHeader(resp.Header.Get("X-CSTP-DPD"))
	result.TLSKeepalive = safeHeader(resp.Header.Get("X-CSTP-Keepalive"))
	result.MTU, err = parseMTU(resp.Header.Get("X-CSTP-MTU"), config.MTU)
	if err != nil {
		return result, err
	}
	var dtlsConn *dtls.Conn
	if config.WithDTLS && resp.Header.Get("X-DTLS-Port") != "" {
		dtlsConn, err = negotiateDTLS(client, sess, resp.Header)
		if err != nil {
			return result, err
		}
		defer dtlsConn.Close()
	}
	result.DTLSEnabled = dtlsConn != nil
	result.DTLSPort = safeHeader(resp.Header.Get("X-DTLS-Port"))
	if dtlsConn != nil {
		result.DTLSPeer = safeAddr(dtlsConn.RemoteAddr())
	}
	interval := headerDuration(result.TLSDPD, config.DPDInterval)
	result.DPDInterval = interval
	observed, err := observeCSTP(ctx, client, sess, config.ObserveFor, interval, dtlsConn, config.Speedtest, resp.Header)
	result.ObservationDuration = observed.Duration
	result.TLSFrames = observed.TLSFrames
	result.DTLSFrames = observed.DTLSFrames
	result.DPDSent = observed.DPDSent
	result.Speedtest = observed.Speedtest
	if dtlsConn != nil {
		result.Transport = "dtls"
	} else {
		result.Transport = "tls"
	}
	if err != nil {
		return result, err
	}
	result.Status = "stable"
	return result, nil
}

func negotiateCSTP(client *acAuth.Client, sess *session.Session, localIPv4 string, mtu int) (*http.Response, error) {
	masterSecret, err := utils.MakeMasterSecret()
	if err != nil {
		return nil, fmt.Errorf("create DTLS master secret: %w", err)
	}
	sess.PreMasterSecret = masterSecret

	req, err := http.NewRequest(http.MethodConnect, client.Prof.Scheme+client.Prof.HostWithPort+"/CSCOSSLC/tunnel", nil)
	if err != nil {
		return nil, fmt.Errorf("create CSTP request: %w", err)
	}
	req.Header.Set("Cookie", "webvpn="+sess.SessionToken)
	req.Header.Set("X-CSTP-VPNAddress-Type", "IPv4")
	req.Header.Set("X-CSTP-MTU", strconv.Itoa(mtu))
	req.Header.Set("X-CSTP-Base-MTU", strconv.Itoa(mtu))
	req.Header.Set("X-CSTP-Local-VPNAddress-IP4", localIPv4)
	req.Header.Set("X-DTLS-Master-Secret", fmt.Sprintf("%x", masterSecret))
	req.Header.Set("X-DTLS12-CipherSuite", "ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:AES128-GCM-SHA256")
	utils.SetCommonHeader(req)

	if err := req.Write(client.Conn); err != nil {
		return nil, fmt.Errorf("write CSTP request: %w", err)
	}
	resp, err := http.ReadResponse(client.BufR, req)
	if err != nil {
		return nil, fmt.Errorf("read CSTP response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("CSTP negotiation failed: %s", resp.Status)
	}
	return resp, nil
}

func observeCSTP(ctx context.Context, client *acAuth.Client, sess *session.Session, observeFor, dpdInterval time.Duration, dtlsConn *dtls.Conn, speedtest *SpeedtestConfig, headers http.Header) (observationResult, error) {
	var observation observationResult
	frames := make(chan cstpFrame, 16)
	errs := make(chan error, 1)
	go readFrames(client.BufR, frames, errs)
	var dtlsFrames chan cstpFrame
	var dtlsErrs chan error
	if dtlsConn != nil {
		dtlsFrames = make(chan cstpFrame, 16)
		dtlsErrs = make(chan error, 1)
		go readDTLSFrames(dtlsConn, dtlsFrames, dtlsErrs)
	}
	writer := &probeWriter{tlsConn: client.Conn, dtlsConn: dtlsConn}
	var bridge *trafficBridge
	var bridgeErrs chan error
	var speedtestDone <-chan speedtestOutcome
	if speedtest != nil {
		var err error
		bridge, err = newTrafficBridge(headers, writer)
		if err != nil {
			return observation, err
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
			observation.TLSFrames++
			if bridge != nil {
				if err := bridge.handleInbound(frame); err != nil {
					return observation, fmt.Errorf("inject CSTP frame into user-space stack: %w", err)
				}
			}
			if frame.Type == 0x05 || frame.Type == 0x09 {
				return observation, fmt.Errorf("server sent CSTP termination frame type=0x%02x", frame.Type)
			}
		case frame := <-dtlsFrames:
			observation.DTLSFrames++
			if bridge != nil {
				if err := bridge.handleInbound(frame); err != nil {
					return observation, fmt.Errorf("inject DTLS frame into user-space stack: %w", err)
				}
			}
			if frame.Type == 0x05 || frame.Type == 0x09 {
				return observation, fmt.Errorf("server sent DTLS termination frame type=0x%02x", frame.Type)
			}
		case err := <-errs:
			return observation, fmt.Errorf("CSTP read failed after %s: %w", time.Since(started).Round(time.Millisecond), err)
		case err := <-dtlsErrs:
			return observation, fmt.Errorf("DTLS read failed after %s: %w", time.Since(started).Round(time.Millisecond), err)
		case err := <-bridgeErrs:
			return observation, fmt.Errorf("user-space outbound traffic failed after %s: %w", time.Since(started).Round(time.Millisecond), err)
		case outcome := <-speedtestDone:
			if outcome.Err != nil {
				return observation, fmt.Errorf("speedtest failed after %s: %w", time.Since(started).Round(time.Millisecond), outcome.Err)
			}
			speedtestFinished = true
			stats := bridge.stats()
			bitsPerSecond := float64(outcome.Result.Bytes*8) / outcome.Result.Duration.Seconds()
			outcome.Result.TargetHost = speedtestHost(speedtest.URL)
			outcome.Result.MiBPS = bitsPerSecond / (1024 * 1024)
			outcome.Result.Transport = transportName(dtlsConn)
			outcome.Result.OutboundFrameBytes = stats.sentBytes
			outcome.Result.InboundFrameBytes = stats.receivedBytes
			outcome.Result.OutboundPackets = stats.sentPackets
			outcome.Result.InboundPackets = stats.receivedPackets
			observation.Speedtest = &outcome.Result
		case <-ticker.C:
			if err := writer.sendControl(0x03); err != nil {
				return observation, fmt.Errorf("send TLS DPD after %s: %w", time.Since(started).Round(time.Millisecond), err)
			}
			observation.DPDSent++
		case <-deadline.C:
			if !speedtestFinished {
				return observation, errors.New("speedtest did not finish within observation window")
			}
			observation.Duration = time.Since(started)
			return observation, nil
		case <-ctx.Done():
			observation.Duration = time.Since(started)
			return observation, ctx.Err()
		}
	}
}

func negotiateDTLS(client *acAuth.Client, sess *session.Session, headers http.Header) (*dtls.Conn, error) {
	port, err := strconv.Atoi(strings.TrimSpace(headers.Get("X-DTLS-Port")))
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid X-DTLS-Port")
	}
	remote, ok := client.Conn.RemoteAddr().(*net.TCPAddr)
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
			ID: id, Secret: sess.PreMasterSecret,
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
	acBase.Info("netcheck DTLS handshake completed", "peer", safeAddr(conn.RemoteAddr()))
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

func runSpeedtest(parent context.Context, tnet *netstack.Net, config SpeedtestConfig) (SpeedtestResult, error) {
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
		return SpeedtestResult{}, err
	}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return SpeedtestResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return SpeedtestResult{}, fmt.Errorf("speedtest returned %s", resp.Status)
	}
	bytesRead, err := io.CopyBuffer(io.Discard, io.LimitReader(resp.Body, config.MaxBytes), make([]byte, 32*1024))
	if err != nil {
		return SpeedtestResult{}, err
	}
	duration := time.Since(started)
	if bytesRead == 0 {
		return SpeedtestResult{}, errors.New("speedtest returned no payload")
	}
	return SpeedtestResult{Bytes: bytesRead, Duration: duration}, nil
}

func NewSpeedtestConfig(rawURL string, maxBytes int64, timeout time.Duration) (*SpeedtestConfig, error) {
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
	return &SpeedtestConfig{URL: parsed.String(), MaxBytes: maxBytes, Timeout: timeout}, nil
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

func parseMTU(raw string, fallback int) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback, nil
	}
	mtu, err := strconv.Atoi(value)
	if err != nil || mtu < 576 || mtu > 65535 {
		return 0, fmt.Errorf("invalid CSTP MTU %q", raw)
	}
	if mtu > fallback {
		return fallback, nil
	}
	return mtu, nil
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

func LoadCredentials(path string) (Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, err
	}
	if len(data) > maxEnvFileBytes {
		return Credentials{}, fmt.Errorf("dotenv file exceeds %d bytes", maxEnvFileBytes)
	}
	values, err := parseDotenv(string(data))
	if err != nil {
		return Credentials{}, err
	}
	creds := Credentials{Endpoint: values["ENDPOINT"], Username: values["USERNAME"], Password: values["PASSWORD"], Group: values["GROUP"]}
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
		return Credentials{}, fmt.Errorf("missing %s", strings.Join(missing, ", "))
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
