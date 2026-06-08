package osnet

import (
	"net/netip"
	"testing"
)

func TestParsePrefixAcceptsCIDRAndIPMask(t *testing.T) {
	tests := map[string]string{
		"10.0.0.0/8":                "10.0.0.0/8",
		"172.16.0.0/255.240.0.0":    "172.16.0.0/12",
		"192.0.2.10":                "192.0.2.10/32",
		"0.0.0.0/0.0.0.0":           "0.0.0.0/0",
		"203.0.113.0/255.255.255.0": "203.0.113.0/24",
	}
	for raw, want := range tests {
		got, err := ParsePrefix(raw)
		if err != nil {
			t.Fatalf("ParsePrefix(%q): %v", raw, err)
		}
		if got.String() != want {
			t.Fatalf("ParsePrefix(%q) = %s, want %s", raw, got, want)
		}
	}
}

func TestParsePrefixRejectsBadInput(t *testing.T) {
	for _, raw := range []string{"", "not-a-route", "10.0.0.0/255.0", "10.0.0.0/33"} {
		if _, err := ParsePrefix(raw); err == nil {
			t.Fatalf("ParsePrefix(%q) succeeded unexpectedly", raw)
		}
	}
}

func TestDiffPrefixes(t *testing.T) {
	old := map[netip.Prefix]bool{
		netip.MustParsePrefix("10.0.0.0/8"):    true,
		netip.MustParsePrefix("172.16.0.0/12"): true,
	}
	next := []netip.Prefix{
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("203.0.113.0/24"),
	}
	add, del, state := DiffPrefixes(old, next)
	if len(add) != 1 || add[0].String() != "203.0.113.0/24" {
		t.Fatalf("add = %v", add)
	}
	if len(del) != 1 || del[0].String() != "10.0.0.0/8" {
		t.Fatalf("del = %v", del)
	}
	if !state[netip.MustParsePrefix("172.16.0.0/12")] || !state[netip.MustParsePrefix("203.0.113.0/24")] {
		t.Fatalf("state = %v", state)
	}
}
