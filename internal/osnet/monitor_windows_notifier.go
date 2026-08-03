//go:build windows

package osnet

import (
	"io"
	"sync"

	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

type windowsUnderlayNotifier struct {
	mu     sync.Mutex
	route  *winipcfg.RouteChangeCallback
	iface  *winipcfg.InterfaceChangeCallback
	closed bool
}

func newWindowsUnderlayNotifier(trigger func()) (io.Closer, error) {
	n := &windowsUnderlayNotifier{}
	route, err := winipcfg.RegisterRouteChangeCallback(func(winipcfg.MibNotificationType, *winipcfg.MibIPforwardRow2) {
		trigger()
	})
	if err != nil {
		return nil, err
	}
	iface, err := winipcfg.RegisterInterfaceChangeCallback(func(winipcfg.MibNotificationType, *winipcfg.MibIPInterfaceRow) {
		trigger()
	})
	if err != nil {
		_ = route.Unregister()
		return nil, err
	}
	n.route = route
	n.iface = iface
	return n, nil
}

func (n *windowsUnderlayNotifier) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil
	}
	n.closed = true
	var first error
	if n.route != nil {
		if err := n.route.Unregister(); err != nil && first == nil {
			first = err
		}
	}
	if n.iface != nil {
		if err := n.iface.Unregister(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
