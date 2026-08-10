package appd

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"flexconnect/internal/buildinfo"
	"flexconnect/internal/router"
	"flexconnect/internal/secret"
	storefile "flexconnect/internal/store/file"
	"flexconnect/internal/types"
	"flexconnect/internal/updater"
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
	mu        sync.Mutex
	events    chan vpn.Event
	connects  int
	failures  []error
	traffic   *types.TrafficStats
	tunnel    vpn.TunnelDialer
	tunnelErr error
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
func (b *fakeBackend) Traffic() *types.TrafficStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.traffic == nil {
		return nil
	}
	traffic := *b.traffic
	return &traffic
}
func (b *fakeBackend) ReadServerConfig() map[string]any { return nil }
func (b *fakeBackend) Events() <-chan vpn.Event         { return b.events }
func (b *fakeBackend) TunnelDialer(context.Context) (vpn.TunnelDialer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tunnelErr != nil {
		return nil, b.tunnelErr
	}
	if b.tunnel == nil {
		return nil, errors.New("no tunnel dialer")
	}
	return b.tunnel, nil
}

func (b *fakeBackend) emit(event vpn.Event) {
	b.events <- event
}

func (b *fakeBackend) connectCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connects
}

func (b *fakeBackend) setTraffic(sent, received uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.traffic = &types.TrafficStats{BytesSent: sent, BytesReceived: received}
}

func TestTrafficSamplesTotalsAndSpeed(t *testing.T) {
	profile := testProfile("p1", false)
	backend := newFakeBackend()
	service := newTestService(t, backend, profile)

	if err := service.Connect(context.Background(), profile.ID); err != nil {
		t.Fatalf("connect: %v", err)
	}
	backend.setTraffic(1000, 2000)
	service.sampleTrafficAt(time.Unix(10, 0).UTC())
	backend.setTraffic(1500, 2600)
	service.sampleTrafficAt(time.Unix(11, 0).UTC())

	traffic := service.Traffic()
	if !traffic.Connected {
		t.Fatal("traffic should be connected")
	}
	if traffic.BytesSent != 1500 || traffic.BytesReceived != 2600 {
		t.Fatalf("totals = sent %d received %d", traffic.BytesSent, traffic.BytesReceived)
	}
	if traffic.BytesSentPerSecond != 500 || traffic.BytesReceivedPerSecond != 600 {
		t.Fatalf("speed = sent %.1f received %.1f", traffic.BytesSentPerSecond, traffic.BytesReceivedPerSecond)
	}
	if traffic.SampledAt != "1970-01-01T00:00:11Z" {
		t.Fatalf("sampled_at = %q", traffic.SampledAt)
	}
}

func TestTrafficClearsOnDisconnect(t *testing.T) {
	profile := testProfile("p1", false)
	backend := newFakeBackend()
	service := newTestService(t, backend, profile)

	if err := service.Connect(context.Background(), profile.ID); err != nil {
		t.Fatalf("connect: %v", err)
	}
	backend.setTraffic(1000, 2000)
	service.sampleTrafficAt(time.Unix(10, 0).UTC())

	if err := service.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	traffic := service.Traffic()
	if traffic.Connected {
		t.Fatal("traffic should be disconnected")
	}
	if traffic.BytesSent != 0 || traffic.BytesReceived != 0 || traffic.BytesSentPerSecond != 0 || traffic.BytesReceivedPerSecond != 0 {
		t.Fatalf("traffic after disconnect = %+v", traffic)
	}
}

func TestAutoReconnectRetriesWithBackoff(t *testing.T) {
	restoreReconnectPolicy(t, 5*time.Millisecond, 10*time.Millisecond, 10)
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

func TestAutoReconnectStopsAfterRetryLimit(t *testing.T) {
	restoreReconnectPolicy(t, time.Millisecond, time.Millisecond, 2)
	profile := testProfile("p1", true)
	backend := newFakeBackend(nil, errors.New("permanent dial failure"), errors.New("permanent dial failure"), nil)
	service := newTestService(t, backend, profile)

	if err := service.Connect(context.Background(), profile.ID); err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	backend.emit(vpn.Event{Type: "disconnected", Err: errors.New("link lost")})

	waitUntil(t, 500*time.Millisecond, func() bool {
		history := service.Diagnostics().ConnectionHistory
		return len(history) > 0 && history[len(history)-1].Kind == "reconnect_exhausted"
	})
	time.Sleep(10 * time.Millisecond)

	if got := backend.connectCount(); got != 3 {
		t.Fatalf("connect count = %d, want initial connection plus 2 retries", got)
	}
	status := service.Status()
	if status.State != types.StateError || status.LastError != "permanent dial failure" {
		t.Fatalf("status after retry exhaustion = %+v", status)
	}
	diagnostics := service.Diagnostics()
	if diagnostics.Reconnect.Active {
		t.Fatalf("reconnect remains active after retry exhaustion: %+v", diagnostics.Reconnect)
	}
	last := diagnostics.ConnectionHistory[len(diagnostics.ConnectionHistory)-1]
	if last.ReasonCode != "retry_limit_reached" || last.Attempt != 2 {
		t.Fatalf("last connection event = %+v", last)
	}
}

func TestUnexpectedDisconnectIsRecordedInDiagnostics(t *testing.T) {
	profile := testProfile("p1", false)
	backend := newFakeBackend()
	service := newTestService(t, backend, profile)
	if err := service.Connect(context.Background(), profile.ID); err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	backend.emit(vpn.Event{Type: "disconnected", Err: errors.New("tls peer closed")})
	waitUntil(t, 500*time.Millisecond, func() bool {
		history := service.Diagnostics().ConnectionHistory
		return len(history) >= 2 && history[len(history)-1].Kind == "connection_lost"
	})
	diagnostics := service.Diagnostics()
	last := diagnostics.ConnectionHistory[len(diagnostics.ConnectionHistory)-1]
	if last.Kind != "connection_lost" || last.ReasonCode != "backend_error" {
		t.Fatalf("last connection event = %+v", last)
	}
	if last.Error != "tls peer closed" {
		t.Fatalf("last connection error = %q", last.Error)
	}
}

func TestManualDisconnectIsNotUnexpectedConnectionLoss(t *testing.T) {
	profile := testProfile("p1", false)
	backend := newFakeBackend()
	service := newTestService(t, backend, profile)
	if err := service.Connect(context.Background(), profile.ID); err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	if err := service.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	backend.emit(vpn.Event{Type: "disconnected"})
	waitUntil(t, 500*time.Millisecond, func() bool {
		return len(service.Diagnostics().ConnectionHistory) >= 1
	})
	last := service.Diagnostics().ConnectionHistory[len(service.Diagnostics().ConnectionHistory)-1]
	if last.Kind == "connection_lost" {
		t.Fatalf("manual disconnect recorded as unexpected: %+v", last)
	}
}

func TestManualDisconnectCancelsScheduledAutoReconnect(t *testing.T) {
	restoreReconnectPolicy(t, 50*time.Millisecond, 50*time.Millisecond, 10)
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

func TestProxyStartsOnlyWithTunnelDialer(t *testing.T) {
	profile := testProfile("p1", false)
	profile.SOCKS5Enabled = true
	profile.SOCKS5Listen = "127.0.0.1:0"
	backend := newFakeBackend()
	backend.tunnel = &noopTunnelDialer{}
	service := newTestService(t, backend, profile)

	if err := service.Connect(context.Background(), profile.ID); err != nil {
		t.Fatalf("connect: %v", err)
	}
	status := service.Status()
	if !status.SOCKS5Enabled {
		t.Fatal("SOCKS5 should be enabled when tunnel dialer is available")
	}
	if status.SOCKS5Listen == "" || status.SOCKS5Listen == "127.0.0.1:0" {
		t.Fatalf("SOCKS5Listen = %q", status.SOCKS5Listen)
	}
	if err := service.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
}

func TestProxyDoesNotFallbackWhenTunnelDialerUnavailable(t *testing.T) {
	profile := testProfile("p1", false)
	profile.SOCKS5Enabled = true
	profile.SOCKS5Listen = "127.0.0.1:0"
	backend := newFakeBackend()
	backend.tunnelErr = errors.New("vpn tunnel unavailable")
	service := newTestService(t, backend, profile)

	if err := service.Connect(context.Background(), profile.ID); err != nil {
		t.Fatalf("connect should not fail the VPN session when proxy cannot start: %v", err)
	}
	status := service.Status()
	if status.SOCKS5Enabled || status.SOCKS5Listen != "" {
		t.Fatalf("SOCKS5 status = enabled %v listen %q, want disabled", status.SOCKS5Enabled, status.SOCKS5Listen)
	}
	if status.LastError == "" {
		t.Fatal("LastError should explain why the VPN-only proxy was not started")
	}
}

func TestCreateProfileReturnsSecretStoreError(t *testing.T) {
	store := &memoryStore{}
	secrets := failingSecretStore{err: errors.New("keyring unavailable")}
	service, err := New(store, secrets, newFakeBackend(), router.DefaultPlanner{})
	if err != nil {
		t.Fatal(err)
	}
	profile := types.NewProfile("corp")
	profile.ServerURL = "https://vpn.example.test"
	profile.Username = "alice"

	_, err = service.CreateProfile(profile, "secret")
	if err == nil {
		t.Fatal("CreateProfile succeeded")
	}
	if !strings.Contains(err.Error(), "keyring unavailable") {
		t.Fatalf("error = %q", err)
	}
	if got := len(service.ListProfiles()); got != 0 {
		t.Fatalf("profiles persisted after secret failure = %d", got)
	}
}

func TestConnectReturnsSecretStoreError(t *testing.T) {
	profile := testProfile("p1", false)
	backend := newFakeBackend()
	store := &memoryStore{data: storefile.Data{Profiles: []types.Profile{profile}, CurrentProfileID: profile.ID}}
	service, err := New(store, failingSecretStore{err: errors.New("keyring unavailable")}, backend, router.DefaultPlanner{})
	if err != nil {
		t.Fatal(err)
	}

	err = service.Connect(context.Background(), profile.ID)
	if err == nil || !strings.Contains(err.Error(), "load secret for profile p1: keyring unavailable") {
		t.Fatalf("Connect error = %v", err)
	}
	if backend.connectCount() != 0 {
		t.Fatalf("backend connect count = %d", backend.connectCount())
	}
}

func TestDeleteProfileReturnsSecretStoreErrorWithoutMutatingProfiles(t *testing.T) {
	profile := testProfile("p1", false)
	store := &memoryStore{data: storefile.Data{Profiles: []types.Profile{profile}, CurrentProfileID: profile.ID}}
	service, err := New(store, failingSecretStore{err: errors.New("keyring unavailable")}, newFakeBackend(), router.DefaultPlanner{})
	if err != nil {
		t.Fatal(err)
	}

	err = service.DeleteProfile(profile.ID)
	if err == nil || !strings.Contains(err.Error(), "delete secret for profile p1: keyring unavailable") {
		t.Fatalf("DeleteProfile error = %v", err)
	}
	if got := service.ListProfiles(); len(got) != 1 || got[0].ID != profile.ID {
		t.Fatalf("profiles after failed delete = %+v", got)
	}
}

type noopTunnelDialer struct{}

func (noopTunnelDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("not used")
}

func (noopTunnelDialer) LookupContextHost(context.Context, string) ([]string, error) {
	return nil, errors.New("not used")
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

func restoreReconnectPolicy(t *testing.T, minDelay, maxDelay time.Duration, maxTries int) {
	t.Helper()
	oldMin, oldMax, oldMaxTries := autoReconnectMinDelay, autoReconnectMaxDelay, autoReconnectMaxTries
	autoReconnectMinDelay, autoReconnectMaxDelay = minDelay, maxDelay
	autoReconnectMaxTries = maxTries
	t.Cleanup(func() {
		autoReconnectMinDelay, autoReconnectMaxDelay = oldMin, oldMax
		autoReconnectMaxTries = oldMaxTries
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

type failingSecretStore struct {
	err error
}

func (s failingSecretStore) Get(string) (string, error) {
	return "", s.err
}

func (s failingSecretStore) Put(string, string) error {
	return s.err
}

func (s failingSecretStore) Delete(string) error {
	return s.err
}

type fakeUpdateChecker struct {
	mu    sync.Mutex
	calls int
	info  updater.Info
}

func (f *fakeUpdateChecker) Check(context.Context) updater.Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.info
}

func (f *fakeUpdateChecker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestUpdateCheckDisabledWithoutRepo(t *testing.T) {
	service := newTestService(t, newFakeBackend(), testProfile("p1", false))
	// No updater configured: the check reports Disabled without error.
	info, err := service.UpdateCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Disabled {
		t.Fatalf("expected Disabled=true, got %+v", info)
	}
	if info.CurrentVersion != buildinfo.Version {
		t.Errorf("CurrentVersion = %q, want %q", info.CurrentVersion, buildinfo.Version)
	}
}

func TestUpdateCheckCachesWithinInterval(t *testing.T) {
	service := newTestService(t, newFakeBackend(), testProfile("p1", false))
	fake := &fakeUpdateChecker{info: updater.Info{
		CurrentVersion:  "1.0.6",
		LatestVersion:   "1.0.7",
		UpdateAvailable: true,
		CheckedAt:       "2026-01-01T00:00:00Z",
	}}
	service.SetUpdater(fake, time.Hour)

	first, err := service.UpdateCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !first.UpdateAvailable {
		t.Fatalf("expected UpdateAvailable=true, got %+v", first)
	}

	second, err := service.UpdateCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !second.UpdateAvailable {
		t.Fatalf("expected cached UpdateAvailable=true, got %+v", second)
	}
	if got := fake.callCount(); got != 1 {
		t.Fatalf("expected 1 checker call (second served from cache), got %d", got)
	}
}
