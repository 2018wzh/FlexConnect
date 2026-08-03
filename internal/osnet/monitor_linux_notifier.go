//go:build linux

package osnet

import (
	"io"
	"sync"

	"github.com/vishvananda/netlink"
)

type linuxUnderlayNotifier struct {
	done  chan struct{}
	once  sync.Once
	error chan error
}

func newLinuxUnderlayNotifier(trigger func()) (io.Closer, error) {
	done := make(chan struct{})
	routeUpdates := make(chan netlink.RouteUpdate, 16)
	addrUpdates := make(chan netlink.AddrUpdate, 16)
	if err := netlink.RouteSubscribe(routeUpdates, done); err != nil {
		close(done)
		return nil, err
	}
	if err := netlink.AddrSubscribe(addrUpdates, done); err != nil {
		close(done)
		return nil, err
	}
	n := &linuxUnderlayNotifier{done: done, error: make(chan error, 1)}
	go func() {
		for {
			select {
			case <-done:
				return
			case _, ok := <-routeUpdates:
				if !ok {
					return
				}
				trigger()
			case _, ok := <-addrUpdates:
				if !ok {
					return
				}
				trigger()
			}
		}
	}()
	return n, nil
}

func (n *linuxUnderlayNotifier) Close() error {
	if n == nil {
		return nil
	}
	n.once.Do(func() { close(n.done) })
	return nil
}
