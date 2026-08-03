package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	acAuth "flexconnect/internal/anyconnect/auth"
	acBase "flexconnect/internal/anyconnect/base"
	"flexconnect/internal/anyconnect/proto"
	"flexconnect/internal/anyconnect/session"
	"flexconnect/internal/anyconnect/utils"
	"flexconnect/internal/osnet"
	"github.com/pion/dtls/v3"
)

const (
	defaultEnvFile    = ".env"
	defaultObserveFor = 35 * time.Second
	maxEnvFileBytes   = 64 * 1024
	maxFrameBytes     = 64 * 1024
)

type credentials struct {
	Endpoint string
	Username string
	Password string
	Group    string
}

type cstpFrame struct {
	Type byte
	Size int
}

func main() {
	envFile := flag.String("env-file", defaultEnvFile, "dotenv file containing ENDPOINT, USERNAME, PASSWORD, and optional GROUP")
	observeFor := flag.Duration("observe", defaultObserveFor, "how long to keep the TLS tunnel under observation")
	dpdInterval := flag.Duration("dpd-interval", 0, "TLS DPD interval; zero derives it from X-CSTP-DPD")
	noDTLS := flag.Bool("no-dtls", false, "do not open the secondary DTLS channel")
	debug := flag.Bool("debug", false, "enable protocol debug logging")
	flag.Parse()

	if *observeFor <= 0 {
		fatal("observe must be positive")
	}
	creds, err := loadCredentials(*envFile)
	if err != nil {
		fatal("load credentials: %v", err)
	}
	if err := run(context.Background(), creds, *observeFor, *dpdInterval, !*noDTLS, *debug); err != nil {
		fatal("probe failed: %v", err)
	}
}

func run(ctx context.Context, creds credentials, observeFor, dpdInterval time.Duration, withDTLS, debug bool) error {
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

	fmt.Printf("probe mode=tls-only no_tun=true endpoint=%s local_ip=%s\n", endpointForOutput(creds.Endpoint), info.IP4)
	if err := acAuth.InitAuth(); err != nil {
		return err
	}
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

	return observeCSTP(ctx, observeFor, interval, dtlsConn)
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

func observeCSTP(ctx context.Context, observeFor, dpdInterval time.Duration, dtlsConn *dtls.Conn) error {
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

	ticker := time.NewTicker(dpdInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(observeFor)
	defer deadline.Stop()
	started := time.Now()
	for {
		select {
		case frame := <-frames:
			fmt.Printf("frame elapsed=%s type=0x%02x payload=%d\n", time.Since(started).Round(time.Millisecond), frame.Type, frame.Size)
			if frame.Type == 0x05 || frame.Type == 0x09 {
				return fmt.Errorf("server sent CSTP termination frame type=0x%02x", frame.Type)
			}
		case frame := <-dtlsFrames:
			fmt.Printf("dtls_frame elapsed=%s type=0x%02x payload=%d\n", time.Since(started).Round(time.Millisecond), frame.Type, frame.Size)
			if frame.Type == 0x05 || frame.Type == 0x09 {
				return fmt.Errorf("server sent DTLS termination frame type=0x%02x", frame.Type)
			}
		case err := <-errs:
			return fmt.Errorf("CSTP read failed after %s: %w", time.Since(started).Round(time.Millisecond), err)
		case err := <-dtlsErrs:
			return fmt.Errorf("DTLS read failed after %s: %w", time.Since(started).Round(time.Millisecond), err)
		case <-ticker.C:
			if err := sendCSTPFrame(0x03); err != nil {
				return fmt.Errorf("send TLS DPD after %s: %w", time.Since(started).Round(time.Millisecond), err)
			}
			if dtlsConn != nil {
				if _, err := dtlsConn.Write([]byte{0x03}); err != nil {
					return fmt.Errorf("send DTLS DPD after %s: %w", time.Since(started).Round(time.Millisecond), err)
				}
			}
			fmt.Printf("dpd elapsed=%s dtls=%t\n", time.Since(started).Round(time.Millisecond), dtlsConn != nil)
		case <-deadline.C:
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
		frames <- cstpFrame{Type: buffer[0], Size: size - 1}
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
		frames <- cstpFrame{Type: header[6], Size: payloadSize}
	}
}

func sendCSTPFrame(frameType byte) error {
	frame := append([]byte(nil), proto.Header...)
	frame[6] = frameType
	frame[4] = 0
	frame[5] = 0
	_, err := acAuth.Conn.Write(frame)
	return err
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

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
