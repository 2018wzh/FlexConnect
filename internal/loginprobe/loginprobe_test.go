package loginprobe

import (
	"strings"
	"testing"
)

func TestServerParts(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		host         string
		hostWithPort string
		groupAccess  string
		wantErr      string
	}{
		{name: "bare host defaults to https and port 443", raw: "vpn.example.com", host: "vpn.example.com", hostWithPort: "vpn.example.com:443", groupAccess: "https://vpn.example.com"},
		{name: "https url with path", raw: "https://vpn.example.com/+CSCOE+/login.html", host: "vpn.example.com", hostWithPort: "vpn.example.com:443", groupAccess: "https://vpn.example.com/+CSCOE+/login.html"},
		{name: "explicit port preserved", raw: "https://vpn.example.com:8443", host: "vpn.example.com:8443", hostWithPort: "vpn.example.com:8443", groupAccess: "https://vpn.example.com:8443"},
		{name: "query and fragment stripped", raw: "https://vpn.example.com/?x=1#frag", host: "vpn.example.com", hostWithPort: "vpn.example.com:443", groupAccess: "https://vpn.example.com"},
		{name: "trailing slash stripped", raw: "https://vpn.example.com/", host: "vpn.example.com", hostWithPort: "vpn.example.com:443", groupAccess: "https://vpn.example.com"},
		{name: "ipv4 host", raw: "203.0.113.7", host: "203.0.113.7", hostWithPort: "203.0.113.7:443", groupAccess: "https://203.0.113.7"},
		{name: "empty", raw: "  ", wantErr: "server URL is empty"},
		{name: "http rejected", raw: "http://vpn.example.com", wantErr: "server URL must use https"},
		{name: "userinfo rejected", raw: "https://alice@vpn.example.com", wantErr: "server URL must not contain user info"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host, hostWithPort, groupAccess, err := serverParts(test.raw)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("serverParts(%q) error = %v, want %q", test.raw, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("serverParts(%q): %v", test.raw, err)
			}
			if host != test.host || hostWithPort != test.hostWithPort || groupAccess != test.groupAccess {
				t.Fatalf("serverParts(%q) = (%q, %q, %q), want (%q, %q, %q)",
					test.raw, host, hostWithPort, groupAccess, test.host, test.hostWithPort, test.groupAccess)
			}
		})
	}
}

func TestVerifyCredentialsRejectsEmptyFields(t *testing.T) {
	if err := VerifyCredentials(t.Context(), Options{ServerURL: "https://vpn.example.com", Password: "pw"}); err == nil || !strings.Contains(err.Error(), "username is empty") {
		t.Fatalf("empty username error = %v", err)
	}
	if err := VerifyCredentials(t.Context(), Options{ServerURL: "https://vpn.example.com", Username: "alice"}); err == nil || !strings.Contains(err.Error(), "password is empty") {
		t.Fatalf("empty password error = %v", err)
	}
}
