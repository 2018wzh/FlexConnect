package auth

import (
	"net"
	"strings"
	"testing"

	"flexconnect/internal/anyconnect/base"
)

func TestRedactAuthBodyRemovesCredentials(t *testing.T) {
	body := `<config-auth><session-token>token-value</session-token><auth><password>password-value</password></auth><message>ok</message></config-auth>`
	got := redactAuthBody(body)
	for _, secret := range []string{"token-value", "password-value", "<session-token>", "<password>"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted body still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "<message>ok</message>") {
		t.Fatalf("non-sensitive response content was removed: %s", got)
	}
}

func TestConfiguredLocalAddrUsesValidatedIPv4(t *testing.T) {
	client := &Client{LocalInterface: base.Interface{Ip4: "192.0.2.10"}}
	addr, ok := client.configuredLocalAddr().(*net.TCPAddr)
	if !ok || !addr.IP.Equal(net.ParseIP("192.0.2.10")) {
		t.Fatalf("configured local address = %#v", addr)
	}

	client.LocalInterface.Ip4 = "not-an-ip"
	if client.configuredLocalAddr() != nil {
		t.Fatal("invalid configured local address was accepted")
	}
}
