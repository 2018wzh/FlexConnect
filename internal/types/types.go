package types

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type State string

const (
	StateDisconnected State = "Disconnected"
	StateConnecting   State = "Connecting"
	StateConnected    State = "Connected"
	StateReconnecting State = "Reconnecting"
	StateError        State = "Error"
)

type RouteSpec struct {
	Destination string `json:"destination"`
	Action      string `json:"action"`
	Metric      int    `json:"metric"`
	Source      string `json:"source"`
	Enabled     bool   `json:"enabled"`
}

type Profile struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	ServerURL          string   `json:"server_url"`
	Username           string   `json:"username"`
	SecretRef          string   `json:"secret_ref"`
	Group              string   `json:"group"`
	AcceptServerRoutes bool     `json:"accept_server_routes"`
	AutoReconnect      *bool    `json:"auto_reconnect"`
	ApplyDNS           *bool    `json:"apply_dns"`
	CustomInclude      []string `json:"custom_include_routes"`
	CustomExclude      []string `json:"custom_exclude_routes"`
	DNSOverrides       []string `json:"dns_overrides"`
	SOCKS5Enabled      bool     `json:"socks5_enabled"`
	SOCKS5Listen       string   `json:"socks5_listen"`
	MTU                int      `json:"mtu"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

type ProfileUpsertRequest struct {
	Profile  Profile `json:"profile"`
	Password string  `json:"password,omitempty"`
}

type ProfileUpdateRequest struct {
	Name               *string  `json:"name,omitempty"`
	ServerURL          *string  `json:"server_url,omitempty"`
	Username           *string  `json:"username,omitempty"`
	Group              *string  `json:"group,omitempty"`
	AcceptServerRoutes *bool    `json:"accept_server_routes,omitempty"`
	AutoReconnect      *bool    `json:"auto_reconnect,omitempty"`
	ApplyDNS           *bool    `json:"apply_dns,omitempty"`
	CustomInclude      []string `json:"custom_include_routes,omitempty"`
	CustomExclude      []string `json:"custom_exclude_routes,omitempty"`
	DNSOverrides       []string `json:"dns_overrides,omitempty"`
	SOCKS5Enabled      *bool    `json:"socks5_enabled,omitempty"`
	SOCKS5Listen       *string  `json:"socks5_listen,omitempty"`
	MTU                *int     `json:"mtu,omitempty"`
	Password           *string  `json:"password,omitempty"`
}

type SessionInfo struct {
	ServerAddress string   `json:"server_address"`
	Hostname      string   `json:"hostname"`
	TUNName       string   `json:"tun_name"`
	VPNAddress    string   `json:"vpn_address"`
	VPNMask       string   `json:"vpn_mask"`
	DNS           []string `json:"dns"`
	MTU           int      `json:"mtu"`
	SplitInclude  []string `json:"split_include"`
	SplitExclude  []string `json:"split_exclude"`
	TLSCipher     string   `json:"tls_cipher"`
	DTLSCipher    string   `json:"dtls_cipher"`
}

type TrafficStats struct {
	BytesSent     uint64 `json:"bytes_sent"`
	BytesReceived uint64 `json:"bytes_received"`
}

type Status struct {
	State              State        `json:"state"`
	CurrentProfileID   string       `json:"current_profile_id"`
	ConnectedProfileID string       `json:"connected_profile_id"`
	Session            *SessionInfo `json:"session,omitempty"`
	EffectiveRoutes    []RouteSpec  `json:"effective_routes,omitempty"`
	LastError          string       `json:"last_error,omitempty"`
	SOCKS5Enabled      bool         `json:"socks5_enabled,omitempty"`
	SOCKS5Listen       string       `json:"socks5_listen,omitempty"`
	UpdatedAt          string       `json:"updated_at"`
}

type RouteUpdateRequest struct {
	AcceptServerRoutes *bool    `json:"accept_server_routes,omitempty"`
	CustomInclude      []string `json:"custom_include_routes,omitempty"`
	CustomExclude      []string `json:"custom_exclude_routes,omitempty"`
}

type LoginRequest struct {
	ProfileID string `json:"profile_id,omitempty"`
	Name      string `json:"name,omitempty"`
	ServerURL string `json:"server_url,omitempty"`
	Username  string `json:"username,omitempty"`
	Group     string `json:"group,omitempty"`
	Password  string `json:"password,omitempty"`
}

type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type Notify struct {
	Version  string     `json:"version"`
	Event    string     `json:"event"`
	Status   *Status    `json:"status,omitempty"`
	Profile  *Profile   `json:"profile,omitempty"`
	Profiles []Profile  `json:"profiles,omitempty"`
	Logs     []LogEntry `json:"logs,omitempty"`
	Message  string     `json:"message,omitempty"`
	Error    string     `json:"error,omitempty"`
	Time     string     `json:"time"`
}

type Diagnostics struct {
	Version        string         `json:"version"`
	Status         Status         `json:"status"`
	CurrentProfile *Profile       `json:"current_profile,omitempty"`
	Profiles       []Profile      `json:"profiles"`
	ServerConfig   map[string]any `json:"server_config,omitempty"`
	Traffic        *TrafficStats  `json:"traffic,omitempty"`
	Logs           []LogEntry     `json:"logs"`
	GeneratedAt    string         `json:"generated_at"`
}

func BoolPtr(v bool) *bool {
	return &v
}

func BoolValue(v *bool, defaultValue bool) bool {
	if v == nil {
		return defaultValue
	}
	return *v
}

func NewProfile(name string) Profile {
	now := time.Now().UTC().Format(time.RFC3339)
	return Profile{
		ID:                 randomID(),
		Name:               name,
		AcceptServerRoutes: true,
		AutoReconnect:      BoolPtr(false),
		ApplyDNS:           BoolPtr(true),
		SOCKS5Listen:       "127.0.0.1:1080",
		MTU:                1399,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

func randomID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
