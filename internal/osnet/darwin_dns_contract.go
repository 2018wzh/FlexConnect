package osnet

import (
	"net/netip"
	"strings"
)

const darwinDNSKey = "State:/Network/Service/FlexConnect/DNS"

func darwinDNSSetScript(servers []netip.Addr) string {
	return "d.init\nd.add ServerAddresses * " + strings.Join(AddrStrings(servers), " ") + "\nset " + darwinDNSKey + "\n"
}

func darwinDNSClearScript() string { return "remove " + darwinDNSKey + "\n" }
