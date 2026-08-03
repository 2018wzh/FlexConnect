package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"flexconnect/client/local"
	"flexconnect/internal/netcheck"
	"flexconnect/internal/types"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestInteractiveLoginStartsTimeoutAfterInput(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	defer inputReader.Close()

	var paths []string
	client := &local.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)

		status := http.StatusOK
		body := "{}"
		if req.URL.Path == "/v1/health" {
			body = `{"status":"ok","version":"1.0.6","api_version":"1"}`
		} else if req.URL.Path == "/v1/login" {
			status = http.StatusNoContent
			body = ""
		} else if req.URL.Path == "/v1/profiles" {
			body = "[]"
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	go func() {
		time.Sleep(40 * time.Millisecond)
		_, _ = io.WriteString(inputWriter, "https://vpn.example.com\nalice\npassword\ncorp\nengineering\n")
		_ = inputWriter.Close()
	}()

	var output strings.Builder
	previousOut := cliOut
	cliOut = &output
	defer func() { cliOut = previousOut }()
	if err := runInteractiveLogin(context.Background(), client, inputReader, &output, 20*time.Millisecond); err != nil {
		t.Fatalf("interactive login failed after slow input: %v", err)
	}

	want := []string{"/v1/login", "/v1/status", "/v1/profiles"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("request paths = %v, want %v", paths, want)
	}
}

func TestRunChecksDaemonBeforeInteractiveLogin(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	defer inputReader.Close()

	var paths []string
	client := &local.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		status := http.StatusOK
		body := "{}"
		if req.URL.Path == "/v1/health" {
			body = `{"status":"ok","version":"1.0.6","api_version":"1"}`
		} else if req.URL.Path == "/v1/login" {
			status = http.StatusNoContent
			body = ""
		} else if req.URL.Path == "/v1/profiles" {
			body = "[]"
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	go func() {
		_, _ = io.WriteString(inputWriter, "https://vpn.example.com\nalice\npassword\ncorp\nengineering\n")
		_ = inputWriter.Close()
	}()

	previousIn, previousOut := cliIn, cliOut
	var output strings.Builder
	cliIn, cliOut = inputReader, &output
	defer func() { cliIn, cliOut = previousIn, previousOut }()
	if err := run(context.Background(), client, []string{"login"}); err != nil {
		t.Fatalf("run interactive login: %v", err)
	}

	want := []string{"/v1/health", "/v1/login", "/v1/status", "/v1/profiles"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("request paths = %v, want %v", paths, want)
	}
}

func TestCommandNeedsDaemon(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{args: []string{"status"}, want: true},
		{args: []string{"login"}, want: true},
		{args: []string{"watch"}, want: true},
		{args: []string{"netcheck"}, want: false},
		{args: []string{"status", "--help"}, want: false},
		{args: []string{"profile", "add", "--help"}, want: false},
		{args: []string{"profile"}, want: false},
		{args: []string{"profile", "list"}, want: true},
		{args: []string{"help", "status"}, want: false},
		{args: []string{"unknown"}, want: false},
	}
	for _, test := range tests {
		if got := commandNeedsDaemon(test.args); got != test.want {
			t.Errorf("commandNeedsDaemon(%v) = %v, want %v", test.args, got, test.want)
		}
	}
}

func TestRunStopsWhenDaemonConnectivityCheckFails(t *testing.T) {
	requestCount := 0
	client := &local.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("daemon unavailable\n")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	err := run(context.Background(), client, []string{"status"})
	if err == nil || !strings.Contains(err.Error(), "cannot connect to flexconnectd: daemon unavailable") {
		t.Fatalf("run error = %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want only the connectivity check", requestCount)
	}
}

func TestReadSecretInputFromStdin(t *testing.T) {
	got, provided, err := readSecretInput("", true, strings.NewReader("test password\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !provided || got != "test password" {
		t.Fatalf("secret = %q provided=%v", got, provided)
	}
}

func TestReadSecretInputRejectsAmbiguousAndOversizedSources(t *testing.T) {
	if _, _, err := readSecretInput("password", true, strings.NewReader("ignored")); err == nil {
		t.Fatal("combined password sources succeeded")
	}
	oversized := strings.NewReader(strings.Repeat("x", maxSecretInputBytes+1))
	if _, _, err := readSecretInput("", true, oversized); err == nil {
		t.Fatal("oversized password input succeeded")
	}
}

func TestFormatTrafficSnapshotText(t *testing.T) {
	got := formatTrafficSnapshot(types.TrafficSnapshot{
		Connected:              true,
		BytesSent:              1024,
		BytesReceived:          2048,
		BytesSentPerSecond:     512,
		BytesReceivedPerSecond: 1536,
		SampledAt:              "2026-06-27T00:00:00Z",
	})

	for _, want := range []string{
		"Connected: true",
		"Traffic Sent: 1024 B",
		"Traffic Received: 2048 B",
		"Speed Sent: 512 B/s",
		"Speed Received: 1536 B/s",
		"Sampled: 2026-06-27T00:00:00Z",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatNetcheckResultTextIncludesSocketsAndSpeed(t *testing.T) {
	got := formatNetcheckResult(netcheck.Result{
		Mode: "CSTP", Endpoint: "vpn.example.test", LocalInterface: "Ethernet",
		LocalIPv4: "192.0.2.10", Gateway: "192.0.2.1", AuthLocalAddress: "192.0.2.10:50000",
		AuthRemoteAddress: "198.51.100.10:443", CSTPStatus: "200 OK", VPNAddress: "172.20.130.149",
		MTU: 1399, Transport: "dtls", ObservationDuration: 2 * time.Second, DPDSent: 1,
		Speedtest: &netcheck.SpeedtestResult{TargetHost: "speed.example.test", Bytes: 1024, Duration: time.Second, MiBPS: 0.01, Transport: "dtls"},
	})
	for _, want := range []string{
		"Auth Socket: 192.0.2.10:50000 -> 198.51.100.10:443",
		"CSTP: 200 OK vpn_ip=172.20.130.149 mtu=1399",
		"Speedtest: target=speed.example.test transport=dtls",
		"0.01 MiB/s",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}
