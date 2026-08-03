//go:build darwin

package osnet

import (
	"context"
	"errors"
	"io"
	"net/netip"
)

func GetUnderlaySnapshot(ctx context.Context, excludeInterface string) (UnderlaySnapshot, error) {
	if ctx == nil {
		return UnderlaySnapshot{}, errors.New("nil underlay context")
	}
	info, err := GetLocalInterface(ctx)
	if err != nil {
		return UnderlaySnapshot{}, err
	}
	if excludeInterface != "" && info.Name == excludeInterface {
		return UnderlaySnapshot{}, errors.New("default route resolves to excluded interface")
	}
	local, err := netip.ParseAddr(info.IP4)
	if err != nil {
		return UnderlaySnapshot{}, err
	}
	gateway, err := netip.ParseAddr(info.Gateway)
	if err != nil {
		return UnderlaySnapshot{}, err
	}
	return UnderlaySnapshot{
		InterfaceName:    info.Name,
		InterfaceIndex:   info.InterfaceIndex,
		LocalIPv4:        local.Unmap(),
		Gateway:          gateway.Unmap(),
		GatewayInterface: info.InterfaceIndex,
	}, nil
}

func newUnderlayNotifier(func()) (io.Closer, error) {
	return nil, errors.New("darwin underlay notifications unavailable; using snapshot polling")
}
