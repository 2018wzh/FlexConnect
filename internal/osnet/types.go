package osnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	wgtun "github.com/tailscale/wireguard-go/tun"
)

type LocalInterface struct {
	Name           string
	IP4            string
	MAC            string
	Gateway        string
	InterfaceIndex int
}

type Config struct {
	InterfaceName         string
	InterfaceIndex        int
	VPNAddress            netip.Prefix
	MTU                   int
	ServerAddress         netip.Addr
	Gateway               netip.Addr
	GatewayInterfaceIndex int
	IncludeRoutes         []netip.Prefix
	ExcludeRoutes         []netip.Prefix
	DNSServers            []netip.Addr
}

type DynamicRoutes struct {
	Include []netip.Addr
	Exclude []netip.Addr
}

type Manager interface {
	Up(context.Context) error
	Set(context.Context, *Config) error
	SetDynamicRoutes(context.Context, DynamicRoutes) error
	Close(context.Context) error
}

func NewManager(dev wgtun.Device, fallbackName string) (Manager, error) {
	name, err := dev.Name()
	if err != nil || name == "" {
		name = fallbackName
	}
	if name == "" {
		return nil, errors.New("missing TUN interface name")
	}
	return newPlatformManager(dev, name)
}

func PrefixFromIPMask(ip, mask string) (netip.Prefix, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return netip.Prefix{}, err
	}
	addr = addr.Unmap()
	parsedMask := net.ParseIP(strings.TrimSpace(mask)).To4()
	if parsedMask == nil {
		return netip.Prefix{}, fmt.Errorf("invalid IPv4 mask: %s", mask)
	}
	ones, bits := net.IPMask(parsedMask).Size()
	if bits != 32 || ones < 0 {
		return netip.Prefix{}, fmt.Errorf("invalid IPv4 mask: %s", mask)
	}
	return netip.PrefixFrom(addr, ones), nil
}

func ParsePrefix(raw string) (netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Prefix{}, errors.New("empty prefix")
	}
	if strings.Contains(raw, "/") {
		parts := strings.Split(raw, "/")
		if len(parts) != 2 {
			return netip.Prefix{}, fmt.Errorf("invalid route prefix: %s", raw)
		}
		if strings.Contains(parts[1], ".") {
			return PrefixFromIPMask(parts[0], parts[1])
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return netip.Prefix{}, err
		}
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Prefix{}, err
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func ParsePrefixes(raw []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := ParsePrefix(value)
		if err != nil {
			return nil, err
		}
		out = append(out, prefix)
	}
	return out, nil
}

func ParseAddrs(raw []string) ([]netip.Addr, error) {
	out := make([]netip.Addr, 0, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		addr, err := netip.ParseAddr(value)
		if err != nil {
			return nil, err
		}
		out = append(out, addr.Unmap())
	}
	return out, nil
}

func PrefixStrings(prefixes []netip.Prefix) []string {
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		out = append(out, prefix.Masked().String())
	}
	return out
}

func AddrStrings(addrs []netip.Addr) []string {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, addr.Unmap().String())
	}
	return out
}

func DiffPrefixes(old map[netip.Prefix]bool, next []netip.Prefix) (add, del []netip.Prefix, state map[netip.Prefix]bool) {
	state = make(map[netip.Prefix]bool, len(next))
	for _, prefix := range next {
		prefix = prefix.Masked()
		state[prefix] = true
		if !old[prefix] {
			add = append(add, prefix)
		}
	}
	for prefix := range old {
		if !state[prefix] {
			del = append(del, prefix)
		}
	}
	return add, del, state
}
