package vpn

import (
	"context"
	"errors"
	"sync"

	"flexconnect/internal/anyconnect/session"
	"flexconnect/internal/osnet"
	wgtun "github.com/tailscale/wireguard-go/tun"
)

// TunnelController owns the operating-system TUN device and its network
// configuration for exactly one ConnSession. Packet workers never close the
// device directly; they report a terminal error to the session and the
// controller performs the ordered shutdown.
type TunnelController struct {
	dev     wgtun.Device
	manager osnet.Manager
	cSess   *session.ConnSession

	stateMu sync.RWMutex
	state   string
	close   sync.Once
	workers sync.WaitGroup
	done    chan struct{}
}

func newTunnelController(dev wgtun.Device, manager osnet.Manager, cSess *session.ConnSession) (*TunnelController, error) {
	if dev == nil {
		return nil, errors.New("nil TUN device")
	}
	if manager == nil {
		return nil, errors.New("nil network manager")
	}
	if cSess == nil {
		return nil, errors.New("nil connection session")
	}
	return &TunnelController{
		dev:     dev,
		manager: manager,
		cSess:   cSess,
		state:   "Configuring",
		done:    make(chan struct{}),
	}, nil
}

func (c *TunnelController) Start() error {
	if c == nil {
		return errors.New("nil tunnel controller")
	}
	c.stateMu.Lock()
	if c.state != "Configuring" {
		state := c.state
		c.stateMu.Unlock()
		return errors.New("cannot start tunnel controller from state " + state)
	}
	c.state = "Running"
	c.stateMu.Unlock()
	c.cSess.SetCloseHook(func() { _ = c.Close(context.Background()) })
	c.cSess.SetTunnelDone(c.done)
	c.cSess.SetLifecycleState("Running")
	c.workers.Add(3)
	go func() {
		defer c.workers.Done()
		watchTunEvents(c.dev, c.cSess)
	}()
	go func() {
		defer c.workers.Done()
		tunToPayloadOut(c.dev, c.cSess)
	}()
	go func() {
		defer c.workers.Done()
		payloadInToTun(c.dev, c.cSess)
	}()
	go func() {
		<-c.cSess.CloseChan
		_ = c.Close(context.Background())
	}()
	return nil
}

func (c *TunnelController) State() string {
	if c == nil {
		return "Closed"
	}
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state
}

func (c *TunnelController) Done() <-chan struct{} {
	if c == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return c.done
}

func (c *TunnelController) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	var firstErr error
	c.close.Do(func() {
		c.stateMu.Lock()
		c.state = "Draining"
		c.stateMu.Unlock()
		c.cSess.SetLifecycleState("Draining")
		if c.manager != nil {
			if err := c.manager.Close(ctx); err != nil {
				firstErr = err
			}
		}
		if c.dev != nil {
			if err := c.dev.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		go func() {
			c.workers.Wait()
			c.stateMu.Lock()
			c.state = "Closed"
			c.stateMu.Unlock()
			c.cSess.SetLifecycleState("Closed")
			close(c.done)
		}()
	})
	return firstErr
}
