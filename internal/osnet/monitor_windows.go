//go:build windows

package osnet

import (
	"context"
	"errors"
	"io"
)

func GetUnderlaySnapshot(ctx context.Context, excludeInterface string) (UnderlaySnapshot, error) {
	if ctx == nil {
		return UnderlaySnapshot{}, errors.New("nil underlay context")
	}
	selected, err := getWindowsDefaultRoute(excludeInterface)
	if err != nil {
		return UnderlaySnapshot{}, err
	}
	return UnderlaySnapshot{
		InterfaceName:    selected.interfaceName,
		InterfaceIndex:   selected.interfaceIndex,
		LocalIPv4:        selected.localIPv4,
		Gateway:          selected.gateway,
		GatewayInterface: selected.interfaceIndex,
		RouteMetric:      selected.effectiveMetric,
	}, nil
}

func newUnderlayNotifier(trigger func()) (io.Closer, error) {
	return newWindowsUnderlayNotifier(trigger)
}
