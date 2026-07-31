package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flexconnect/internal/buildinfo"
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
func (fakeDaemon) UpdateCheck(context.Context) (types.UpdateInfo, error) {
	return types.UpdateInfo{
		CurrentVersion:  "1.0.6",
		LatestVersion:   "1.0.7",
		UpdateAvailable: true,
		ReleaseURL:      "https://github.com/owner/repo/releases/tag/v1.0.7",
		CheckedAt:       "2026-06-27T00:00:00Z",
	}, nil
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

func TestHealthEndpointReturnsVersionedReadiness(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rec := httptest.NewRecorder()

	New(fakeDaemon{}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got types.Health
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" || got.Version != buildinfo.Version || got.APIVersion != buildinfo.LocalAPIVersion {
		t.Fatalf("health = %+v", got)
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

func TestRequestBodyIsBounded(t *testing.T) {
	body := bytes.NewReader(bytes.Repeat([]byte("x"), maxRequestBodyBytes+1))
	req := httptest.NewRequest(http.MethodPost, "/v1/login", body)
	rec := httptest.NewRecorder()

	New(fakeDaemon{}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", rec.Code)
	}
	if requestID := rec.Header().Get("X-FlexConnect-Request-ID"); requestID == "" {
		t.Fatal("missing request ID")
	}
}

func TestLoginRejectsUnknownJSONFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"server_url":"vpn.example.test","unknown":true}`))
	rec := httptest.NewRecorder()

	New(fakeDaemon{}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestUpdateCheckEndpoint(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/update/check", nil)
	rec := httptest.NewRecorder()

	New(fakeDaemon{}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got types.UpdateInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.CurrentVersion != "1.0.6" || got.LatestVersion != "1.0.7" {
		t.Fatalf("versions = %q/%q", got.CurrentVersion, got.LatestVersion)
	}
	if !got.UpdateAvailable {
		t.Fatal("expected update_available=true")
	}
	if got.ReleaseURL == "" {
		t.Fatal("expected release_url to be set")
	}
}

func TestUpdateCheckEndpointRejectsNonGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/update/check", nil)
	rec := httptest.NewRecorder()

	New(fakeDaemon{}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
}
