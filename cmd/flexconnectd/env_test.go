package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"flexconnect/internal/types"
)

func TestParseDaemonOptionsUsesEnvDefaultsAndFlagOverrides(t *testing.T) {
	env := mapEnv{
		"FLEXCONNECT_SOCKET":       "/run/flexconnect/env.sock",
		"FLEXCONNECT_STATE":        "/var/lib/flexconnect/env-state.json",
		"FLEXCONNECT_VERBOSE":      "true",
		"FLEXCONNECT_SECRET_STORE": "memory",
	}

	opts, err := parseDaemonOptions([]string{"--socket", "/run/flexconnect/flag.sock"}, env.lookup)
	if err != nil {
		t.Fatalf("parseDaemonOptions: %v", err)
	}

	if opts.socket != "/run/flexconnect/flag.sock" {
		t.Fatalf("socket = %q", opts.socket)
	}
	if opts.state != "/var/lib/flexconnect/env-state.json" {
		t.Fatalf("state = %q", opts.state)
	}
	if !opts.verbose {
		t.Fatal("verbose should come from FLEXCONNECT_VERBOSE")
	}
	if opts.secretStore != "memory" {
		t.Fatalf("secretStore = %q", opts.secretStore)
	}
}

func TestParseDaemonOptionsRejectsInvalidEnv(t *testing.T) {
	tests := []struct {
		name string
		env  mapEnv
		want string
	}{
		{
			name: "bad verbose bool",
			env:  mapEnv{"FLEXCONNECT_VERBOSE": "maybe"},
			want: "FLEXCONNECT_VERBOSE",
		},
		{
			name: "bad secret store",
			env:  mapEnv{"FLEXCONNECT_SECRET_STORE": "file"},
			want: "FLEXCONNECT_SECRET_STORE",
		},
		{
			name: "bad connect timeout",
			env: mapEnv{
				"FLEXCONNECT_CONNECT_ON_START": "true",
				"FLEXCONNECT_SERVER":           "vpn.example.test",
				"FLEXCONNECT_USERNAME":         "alice",
				"FLEXCONNECT_PASSWORD":         "secret",
				"FLEXCONNECT_CONNECT_TIMEOUT":  "slow",
			},
			want: "FLEXCONNECT_CONNECT_TIMEOUT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseDaemonOptions(nil, tt.env.lookup)
			if err == nil {
				t.Fatal("parseDaemonOptions succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want mention %q", err, tt.want)
			}
		})
	}
}

func TestParseStartupProfileFromEnv(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := mapEnv{
		"FLEXCONNECT_CONNECT_ON_START":     "true",
		"FLEXCONNECT_PROFILE_ID":           "corp",
		"FLEXCONNECT_PROFILE_NAME":         "Corp VPN",
		"FLEXCONNECT_SERVER":               "vpn.example.test",
		"FLEXCONNECT_USERNAME":             "alice",
		"FLEXCONNECT_GROUP":                "employees",
		"FLEXCONNECT_PASSWORD_FILE":        passwordFile,
		"FLEXCONNECT_ACCEPT_SERVER_ROUTES": "false",
		"FLEXCONNECT_AUTO_RECONNECT":       "true",
		"FLEXCONNECT_APPLY_DNS":            "false",
		"FLEXCONNECT_MTU":                  "1300",
		"FLEXCONNECT_DNS":                  "10.0.0.53, 10.0.0.54",
		"FLEXCONNECT_INCLUDE_ROUTES":       "10.0.0.0/8, 172.16.0.0/12",
		"FLEXCONNECT_EXCLUDE_ROUTES":       "192.168.0.0/16",
		"FLEXCONNECT_SOCKS5_ENABLED":       "true",
		"FLEXCONNECT_SOCKS5_LISTEN":        "0.0.0.0:1080",
		"FLEXCONNECT_CONNECT_TIMEOUT":      "45s",
	}

	opts, err := parseDaemonOptions(nil, env.lookup)
	if err != nil {
		t.Fatalf("parseDaemonOptions: %v", err)
	}
	boot := opts.startup
	if boot == nil {
		t.Fatal("startup config is nil")
	}

	if !boot.ConnectOnStart {
		t.Fatal("ConnectOnStart should be true")
	}
	if boot.Password != "secret" {
		t.Fatalf("Password = %q", boot.Password)
	}
	if boot.ConnectTimeout != 45*time.Second {
		t.Fatalf("ConnectTimeout = %s", boot.ConnectTimeout)
	}
	if !boot.RequireSOCKS5 {
		t.Fatal("RequireSOCKS5 should be true")
	}
	profile := boot.Profile
	if profile.ID != "corp" || profile.Name != "Corp VPN" || profile.ServerURL != "vpn.example.test" || profile.Username != "alice" || profile.Group != "employees" {
		t.Fatalf("profile identity = %+v", profile)
	}
	if profile.AcceptServerRoutes {
		t.Fatal("AcceptServerRoutes should be false")
	}
	if !types.BoolValue(profile.AutoReconnect, false) {
		t.Fatal("AutoReconnect should be true")
	}
	if types.BoolValue(profile.ApplyDNS, true) {
		t.Fatal("ApplyDNS should be false")
	}
	if profile.MTU != 1300 {
		t.Fatalf("MTU = %d", profile.MTU)
	}
	if !reflect.DeepEqual(profile.DNSOverrides, []string{"10.0.0.53", "10.0.0.54"}) {
		t.Fatalf("DNSOverrides = %#v", profile.DNSOverrides)
	}
	if !reflect.DeepEqual(profile.CustomInclude, []string{"10.0.0.0/8", "172.16.0.0/12"}) {
		t.Fatalf("CustomInclude = %#v", profile.CustomInclude)
	}
	if !reflect.DeepEqual(profile.CustomExclude, []string{"192.168.0.0/16"}) {
		t.Fatalf("CustomExclude = %#v", profile.CustomExclude)
	}
	if !profile.SOCKS5Enabled || profile.SOCKS5Listen != "0.0.0.0:1080" {
		t.Fatalf("SOCKS5 = enabled %v listen %q", profile.SOCKS5Enabled, profile.SOCKS5Listen)
	}
}

func TestParseStartupProfileRequiresConnectionFields(t *testing.T) {
	env := mapEnv{"FLEXCONNECT_CONNECT_ON_START": "true"}

	_, err := parseDaemonOptions(nil, env.lookup)
	if err == nil {
		t.Fatal("parseDaemonOptions succeeded")
	}
	for _, want := range []string{"FLEXCONNECT_SERVER", "FLEXCONNECT_USERNAME", "FLEXCONNECT_PASSWORD"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want mention %q", err, want)
		}
	}
}

func TestParseStartupProfileRejectsPasswordEnvAndFileTogether(t *testing.T) {
	env := mapEnv{
		"FLEXCONNECT_CONNECT_ON_START": "true",
		"FLEXCONNECT_SERVER":           "vpn.example.test",
		"FLEXCONNECT_USERNAME":         "alice",
		"FLEXCONNECT_PASSWORD":         "secret",
		"FLEXCONNECT_PASSWORD_FILE":    "secret.txt",
	}

	_, err := parseDaemonOptions(nil, env.lookup)
	if err == nil {
		t.Fatal("parseDaemonOptions succeeded")
	}
	if !strings.Contains(err.Error(), "FLEXCONNECT_PASSWORD") || !strings.Contains(err.Error(), "FLEXCONNECT_PASSWORD_FILE") {
		t.Fatalf("error = %q", err)
	}
}

func TestParseStartupProfileReturnsPasswordFileError(t *testing.T) {
	env := mapEnv{
		"FLEXCONNECT_CONNECT_ON_START": "true",
		"FLEXCONNECT_SERVER":           "vpn.example.test",
		"FLEXCONNECT_USERNAME":         "alice",
		"FLEXCONNECT_PASSWORD_FILE":    filepath.Join(t.TempDir(), "missing"),
	}

	_, err := parseDaemonOptions(nil, env.lookup)
	if err == nil {
		t.Fatal("parseDaemonOptions succeeded")
	}
	if !strings.Contains(err.Error(), "FLEXCONNECT_PASSWORD_FILE") {
		t.Fatalf("error = %q", err)
	}
}

func TestBootstrapStartupCreatesAndConnectsProfile(t *testing.T) {
	daemon := &fakeStartupDaemon{}
	boot := &startupConfig{
		ConnectOnStart: true,
		Profile: types.Profile{
			ID:            "docker",
			Name:          "docker",
			ServerURL:     "vpn.example.test",
			Username:      "alice",
			SecretRef:     "profile/docker",
			SOCKS5Enabled: true,
			SOCKS5Listen:  "0.0.0.0:1080",
		},
		Password:       "secret",
		ConnectTimeout: time.Second,
		RequireSOCKS5:  true,
	}
	daemon.status = types.Status{SOCKS5Enabled: true, SOCKS5Listen: "0.0.0.0:1080"}

	if err := bootstrapStartup(context.Background(), daemon, boot); err != nil {
		t.Fatalf("bootstrapStartup: %v", err)
	}

	if len(daemon.created) != 1 {
		t.Fatalf("created count = %d", len(daemon.created))
	}
	if daemon.created[0].ID != "docker" || daemon.createdPassword != "secret" {
		t.Fatalf("created = %+v password=%q", daemon.created[0], daemon.createdPassword)
	}
	if daemon.switchedID != "docker" {
		t.Fatalf("switchedID = %q", daemon.switchedID)
	}
	if daemon.connectedID != "docker" {
		t.Fatalf("connectedID = %q", daemon.connectedID)
	}
}

func TestBootstrapStartupUpdatesExistingProfile(t *testing.T) {
	daemon := &fakeStartupDaemon{
		profiles: []types.Profile{{ID: "docker", Name: "old", SecretRef: "profile/docker"}},
		status:   types.Status{SOCKS5Enabled: true, SOCKS5Listen: "0.0.0.0:1080"},
	}
	boot := &startupConfig{
		ConnectOnStart: true,
		Profile: types.Profile{
			ID:            "docker",
			Name:          "new",
			ServerURL:     "vpn.example.test",
			Username:      "alice",
			SecretRef:     "profile/docker",
			SOCKS5Enabled: true,
			SOCKS5Listen:  "0.0.0.0:1080",
		},
		Password:       "secret",
		ConnectTimeout: time.Second,
		RequireSOCKS5:  true,
	}

	if err := bootstrapStartup(context.Background(), daemon, boot); err != nil {
		t.Fatalf("bootstrapStartup: %v", err)
	}

	if len(daemon.created) != 0 {
		t.Fatalf("created count = %d", len(daemon.created))
	}
	if daemon.updatedID != "docker" {
		t.Fatalf("updatedID = %q", daemon.updatedID)
	}
	if daemon.updatedReq.Name == nil || *daemon.updatedReq.Name != "new" {
		t.Fatalf("updated name = %#v", daemon.updatedReq.Name)
	}
	if daemon.updatedReq.Password == nil || *daemon.updatedReq.Password != "secret" {
		t.Fatalf("updated password = %#v", daemon.updatedReq.Password)
	}
	if daemon.connectedID != "docker" {
		t.Fatalf("connectedID = %q", daemon.connectedID)
	}
}

func TestBootstrapStartupReturnsConnectError(t *testing.T) {
	daemon := &fakeStartupDaemon{connectErr: errors.New("vpn failed")}
	boot := &startupConfig{
		ConnectOnStart: true,
		Profile:        types.Profile{ID: "docker", SecretRef: "profile/docker"},
		Password:       "secret",
		ConnectTimeout: time.Second,
	}

	err := bootstrapStartup(context.Background(), daemon, boot)
	if err == nil {
		t.Fatal("bootstrapStartup succeeded")
	}
	if !strings.Contains(err.Error(), "vpn failed") {
		t.Fatalf("error = %q", err)
	}
}

func TestBootstrapStartupFailsWhenRequiredSOCKS5IsNotListening(t *testing.T) {
	daemon := &fakeStartupDaemon{status: types.Status{SOCKS5Enabled: false}}
	boot := &startupConfig{
		ConnectOnStart: true,
		Profile:        types.Profile{ID: "docker", SecretRef: "profile/docker", SOCKS5Enabled: true, SOCKS5Listen: "0.0.0.0:1080"},
		Password:       "secret",
		ConnectTimeout: time.Second,
		RequireSOCKS5:  true,
	}

	err := bootstrapStartup(context.Background(), daemon, boot)
	if err == nil {
		t.Fatal("bootstrapStartup succeeded")
	}
	if !strings.Contains(err.Error(), "SOCKS5") {
		t.Fatalf("error = %q", err)
	}
}

type mapEnv map[string]string

func (m mapEnv) lookup(key string) (string, bool) {
	v, ok := m[key]
	return v, ok
}

type fakeStartupDaemon struct {
	profiles        []types.Profile
	status          types.Status
	created         []types.Profile
	createdPassword string
	updatedID       string
	updatedReq      types.ProfileUpdateRequest
	switchedID      string
	connectedID     string
	connectErr      error
}

func (d *fakeStartupDaemon) ListProfiles() []types.Profile {
	return append([]types.Profile(nil), d.profiles...)
}

func (d *fakeStartupDaemon) CreateProfile(profile types.Profile, password string) (types.Profile, error) {
	d.created = append(d.created, profile)
	d.createdPassword = password
	d.profiles = append(d.profiles, profile)
	return profile, nil
}

func (d *fakeStartupDaemon) UpdateProfile(id string, req types.ProfileUpdateRequest) (types.Profile, error) {
	d.updatedID = id
	d.updatedReq = req
	for i := range d.profiles {
		if d.profiles[i].ID == id {
			if req.Name != nil {
				d.profiles[i].Name = *req.Name
			}
			if req.ServerURL != nil {
				d.profiles[i].ServerURL = *req.ServerURL
			}
			if req.Username != nil {
				d.profiles[i].Username = *req.Username
			}
			if req.Group != nil {
				d.profiles[i].Group = *req.Group
			}
			if req.AcceptServerRoutes != nil {
				d.profiles[i].AcceptServerRoutes = *req.AcceptServerRoutes
			}
			if req.AutoReconnect != nil {
				d.profiles[i].AutoReconnect = req.AutoReconnect
			}
			if req.ApplyDNS != nil {
				d.profiles[i].ApplyDNS = req.ApplyDNS
			}
			if req.CustomInclude != nil {
				d.profiles[i].CustomInclude = req.CustomInclude
			}
			if req.CustomExclude != nil {
				d.profiles[i].CustomExclude = req.CustomExclude
			}
			if req.DNSOverrides != nil {
				d.profiles[i].DNSOverrides = req.DNSOverrides
			}
			if req.SOCKS5Enabled != nil {
				d.profiles[i].SOCKS5Enabled = *req.SOCKS5Enabled
			}
			if req.SOCKS5Listen != nil {
				d.profiles[i].SOCKS5Listen = *req.SOCKS5Listen
			}
			if req.MTU != nil {
				d.profiles[i].MTU = *req.MTU
			}
			return d.profiles[i], nil
		}
	}
	return types.Profile{}, errors.New("profile not found")
}

func (d *fakeStartupDaemon) SwitchProfile(_ context.Context, id string) error {
	d.switchedID = id
	return nil
}

func (d *fakeStartupDaemon) Connect(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.connectedID = id
	return d.connectErr
}

func (d *fakeStartupDaemon) Status() types.Status {
	return d.status
}
