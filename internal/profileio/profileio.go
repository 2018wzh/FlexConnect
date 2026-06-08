package profileio

import (
	"net/url"
	"strings"

	"flexconnect/internal/types"
)

func NormalizeProfile(profile types.Profile) types.Profile {
	profile.ServerURL = NormalizeServerURL(profile.ServerURL)
	if profile.ID == "" {
		profile.ID = types.NewProfile(profile.Name).ID
	}
	if profile.Name == "" {
		profile.Name = DefaultProfileName(profile.ServerURL, profile.Username)
	}
	if profile.AutoReconnect == nil {
		profile.AutoReconnect = types.BoolPtr(false)
	}
	if profile.ApplyDNS == nil {
		profile.ApplyDNS = types.BoolPtr(true)
	}
	return profile
}

func NormalizeServerURL(v string) string {
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://") {
		return v
	}
	return "https://" + v
}

func DefaultProfileName(serverURL, username string) string {
	host := serverURL
	if parsed, err := url.Parse(serverURL); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	host = strings.TrimSpace(host)
	username = strings.TrimSpace(username)
	if username != "" && host != "" {
		return username + "@" + host
	}
	if host != "" {
		return host
	}
	if username != "" {
		return username
	}
	return "Profile"
}
