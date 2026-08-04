package vpn

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"flexconnect/internal/anyconnect/session"
	"flexconnect/internal/osnet"
)

type fakeManager struct {
	mu       sync.Mutex
	closed   int
	closeErr error
}

func (m *fakeManager) Up(context.Context) error { return nil }
func (m *fakeManager) Set(context.Context, *osnet.Config) error {
	return nil
}
func (m *fakeManager) SetDynamicRoutes(context.Context, osnet.DynamicRoutes) error {
	return nil
}
func (m *fakeManager) Close(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed++
	return m.closeErr
}

func (m *fakeManager) closeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func TestTunnelControllerDrainsAndSignalsDone(t *testing.T) {
	sess := &session.Session{}
	cSess := sess.NewConnSession(&http.Header{})
	dev := newTestTUNDevice()
	dev.readErr = errors.New("device closed")
	manager := &fakeManager{}

	controller, err := newTunnelController(dev, manager, cSess)
	if err != nil {
		t.Fatalf("newTunnelController: %v", err)
	}
	if err := controller.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := cSess.TunnelDone()
	if done == nil {
		t.Fatal("controller did not register the tunnel drain channel")
	}

	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel controller did not finish draining")
	}
	if controller.State() != "Closed" {
		t.Fatalf("controller state = %s, want Closed", controller.State())
	}
	if manager.closeCount() != 1 {
		t.Fatalf("manager closed %d times, want 1", manager.closeCount())
	}
}

func TestTunnelControllerCloseIsIdempotent(t *testing.T) {
	sess := &session.Session{}
	cSess := sess.NewConnSession(&http.Header{})
	dev := newTestTUNDevice()
	dev.readErr = errors.New("device closed")
	manager := &fakeManager{closeErr: errors.New("cleanup failed")}

	controller, err := newTunnelController(dev, manager, cSess)
	if err != nil {
		t.Fatalf("newTunnelController: %v", err)
	}
	if err := controller.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	first := controller.Close(context.Background())
	if first == nil {
		t.Fatal("first close should surface the manager error")
	}
	if second := controller.Close(context.Background()); second != nil {
		t.Fatalf("second close returned %v, want nil", second)
	}
	if manager.closeCount() != 1 {
		t.Fatalf("manager closed %d times, want exactly 1", manager.closeCount())
	}
}
