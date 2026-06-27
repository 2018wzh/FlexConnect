package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"flexconnect/internal/types"
)

type fakeDaemon struct{}

func (fakeDaemon) Status() types.Status                   { return types.Status{State: types.StateDisconnected} }
func (fakeDaemon) ListProfiles() []types.Profile          { return nil }
func (fakeDaemon) CurrentProfile() (types.Profile, error) { return types.Profile{}, nil }
func (fakeDaemon) CreateProfile(types.Profile, string) (types.Profile, error) {
	return types.Profile{}, nil
}
func (fakeDaemon) UpdateProfile(string, types.ProfileUpdateRequest) (types.Profile, error) {
	return types.Profile{}, nil
}
func (fakeDaemon) DeleteProfile(string) error                  { return nil }
func (fakeDaemon) SwitchProfile(context.Context, string) error { return nil }
func (fakeDaemon) ConnectCurrent(context.Context) error        { return nil }
func (fakeDaemon) Connect(context.Context, string) error       { return nil }
func (fakeDaemon) Disconnect(context.Context) error            { return nil }
func (fakeDaemon) UpdateRoutes(string, types.RouteUpdateRequest) (types.Profile, error) {
	return types.Profile{}, nil
}
func (fakeDaemon) Login(context.Context, types.LoginRequest) error { return nil }
func (fakeDaemon) Diagnostics() types.Diagnostics                  { return types.Diagnostics{} }
func (fakeDaemon) Logs() []types.LogEntry                          { return nil }
func (fakeDaemon) Watch(context.Context) <-chan types.Notify       { return make(chan types.Notify) }
func (fakeDaemon) Traffic() types.TrafficSnapshot {
	return types.TrafficSnapshot{Connected: false, SampledAt: "2026-06-27T00:00:00Z"}
}

func TestTrafficEndpointReturnsSnapshot(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/traffic", nil)
	rec := httptest.NewRecorder()

	New(fakeDaemon{}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got types.TrafficSnapshot
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Connected {
		t.Fatal("traffic should be disconnected")
	}
	if got.SampledAt != "2026-06-27T00:00:00Z" {
		t.Fatalf("sampled_at = %q", got.SampledAt)
	}
}

func TestTrafficEndpointRejectsNonGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/traffic", nil)
	rec := httptest.NewRecorder()

	New(fakeDaemon{}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
}
