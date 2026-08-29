package osnet

import (
	"net/netip"
	"strings"
	"testing"
)

func TestDarwinDNSUsesFixedDynamicStoreKeyAndStructuredStdin(t *testing.T) {
	script := darwinDNSSetScript([]netip.Addr{netip.MustParseAddr("10.0.0.53"), netip.MustParseAddr("10.0.0.54")})
	for _, want := range []string{"d.init\n", "d.add ServerAddresses * 10.0.0.53 10.0.0.54\n", "set " + darwinDNSKey + "\n"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script %q is missing %q", script, want)
		}
	}
	if got := darwinDNSClearScript(); got != "remove "+darwinDNSKey+"\n" {
		t.Fatalf("clear script = %q", got)
	}
}
