package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"flexconnect/internal/anyconnect/base"
)

func TestAuthenticationResponseLimit(t *testing.T) {
	if _, err := readAuthResponse(bytes.NewReader(make([]byte, maxAuthResponseBytes+1))); err == nil || !strings.Contains(err.Error(), "authentication response exceeds 4194304 bytes") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestCertificateFailureOccursBeforeCredentialsAreSent(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(Profile{HostWithPort: u.Host, Username: "alice", Password: "secret"}, base.Interface{})
	if err := client.InitAuth(nil); err == nil {
		t.Fatal("self-signed server was trusted")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("HTTP credential-bearing requests = %d, want 0", got)
	}
}
