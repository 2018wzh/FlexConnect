package appd

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"flexconnect/internal/router"
	"flexconnect/internal/secret"
	storefile "flexconnect/internal/store/file"
	"flexconnect/internal/types"
	"flexconnect/internal/vpn"
)

type memoryStore struct {
	mu   sync.Mutex
	data storefile.Data
}

func (s *memoryStore) Load() (storefile.Data, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data, nil
}

func (s *memoryStore) Save(data storefile.Data) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = data
	return nil
}

type fakeBackend struct {
	mu       sync.Mutex
	events   chan vpn.Event
	connects int
	failures []error
}

func newFakeBackend(failures ...error) *fakeBackend {
	return &fakeBackend{events: make(chan vpn.Event, 16), failures: failures}
}

func (b *fakeBackend) Connect(context.Context, types.Profile, string) (*types.SessionInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connects++
	if len(b.failures) > 0 {
		err := b.failures[0]
		b.failures = b.failures[1:]
		if err != nil {
			return nil, err
		}
	}
	return &types.SessionInfo{ServerAddress: "vpn.example.test", VPNAddress: "10.0.0.2"}, nil
}

func (b *fakeBackend) Disconnect(context.Context) error { return nil }
func (b *fakeBackend) SessionInfo() *types.SessionInfo  { return nil }
func (b *fakeBackend) Traffic() *types.TrafficStats     { return nil }
func (b *fakeBackend) ReadServerConfig() map[string]any { return nil }
func (b *fakeBackend) Events() <-chan vpn.Event         { return b.events }

func (b *fakeBackend) emit(event vpn.Event) {
	b.events <- event
}

func (b *fakeBackend) connectCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connects
}

func TestAutoReconnectRetriesWithBackoff(t *testing.T) {
	restoreReconnectDelays(t, 5*time.Millisecond, 10*time.Millisecond)
	profile := testProfile("p1", true)
	backend := newFakeBackend(nil, errors.New("temporary dial failure"), nil)
	service := newTestService(t, backend, profile)

	if err := service.Connect(context.Background(), profile.ID); err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	backend.emit(vpn.Event{Type: "disconnected", Err: errors.New("link lost")})

	waitUntil(t, 500*time.Millisecond, func() bool {
		return backend.connectCount() >= 3 && service.Status().State == types.StateConnected
	})
}

func TestManualDisconnectCancelsScheduledAutoReconnect(t *testing.T) {
	restoreReconnectDelays(t, 50*time.Millisecond, 50*time.Millisecond)
	profile := testProfile("p1", true)
	backend := newFakeBackend()
	service := newTestService(t, backend, profile)

	if err := service.Connect(context.Background(), profile.ID); err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	backend.emit(vpn.Event{Type: "disconnected", Err: errors.New("link lost")})

	waitUntil(t, 500*time.Millisecond, func() bool {
		service.mu.Lock()
		defer service.mu.Unlock()
		return service.reconnectTimer != nil
	})
	if err := service.Disconnect(context.Background()); err != nil {
		t.Fatalf("manual disconnect: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if got := backend.connectCount(); got != 1 {
		t.Fatalf("connect count = %d, want 1", got)
	}
}

func newTestService(t *testing.T, backend *fakeBackend, profiles ...types.Profile) *Service {
	t.Helper()
	store := &memoryStore{data: storefile.Data{Profiles: profiles, CurrentProfileID: profiles[0].ID}}
	secrets := secret.NewMemoryStore()
	for _, profile := range profiles {
		if err := secrets.Put(profile.SecretRef, "password"); err != nil {
			t.Fatal(err)
		}
	}
	service, err := New(store, secrets, backend, router.DefaultPlanner{})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testProfile(id string, autoReconnect bool) types.Profile {
	return types.Profile{
		ID:                 id,
		Name:               "test",
		ServerURL:          "https://vpn.example.test",
		Username:           "alice",
		SecretRef:          "profile/" + id,
		AcceptServerRoutes: true,
		AutoReconnect:      types.BoolPtr(autoReconnect),
		ApplyDNS:           types.BoolPtr(true),
		SOCKS5Listen:       "127.0.0.1:1080",
		MTU:                1399,
	}
}

func restoreReconnectDelays(t *testing.T, minDelay, maxDelay time.Duration) {
	t.Helper()
	oldMin, oldMax := autoReconnectMinDelay, autoReconnectMaxDelay
	autoReconnectMinDelay, autoReconnectMaxDelay = minDelay, maxDelay
	t.Cleanup(func() {
		autoReconnectMinDelay, autoReconnectMaxDelay = oldMin, oldMax
	})
}

func waitUntil(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
