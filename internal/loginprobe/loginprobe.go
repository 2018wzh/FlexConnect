// Package loginprobe performs bounded pre-flight authentication probes for
// interactive CLI logins. Probes exercise only the AnyConnect web
// authentication flow: they never create an OS TUN device and never establish
// the VPN tunnel.
package loginprobe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	acAuth "flexconnect/internal/anyconnect/auth"
	acBase "flexconnect/internal/anyconnect/base"
	"flexconnect/internal/anyconnect/session"
	"flexconnect/internal/osnet"
)

// DefaultTimeout bounds each probe's authentication exchange.
const DefaultTimeout = 45 * time.Second

// Options describes one credential verification probe.
type Options struct {
	ServerURL string
	Group     string
	Username  string
	Password  string
	Timeout   time.Duration
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return DefaultTimeout
	}
	return o.Timeout
}

// FetchGroups verifies that serverURL reaches an AnyConnect endpoint and
// returns the user groups advertised by its login form. The group list is
// empty when the server does not offer a group selection.
func FetchGroups(ctx context.Context, serverURL string) ([]string, error) {
	client, err := newAuthClient(ctx, serverURL)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if err := client.InitAuth(nil); err != nil {
		// InitAuth rejects an empty profile group whenever the server
		// advertises one, but the form (and its groups) was still received,
		// which is all this probe needs.
		if groups := client.ServerGroups(); len(groups) != 0 {
			return groups, nil
		}
		return nil, err
	}
	return client.ServerGroups(), nil
}

// VerifyCredentials performs a complete password authentication against the
// server without establishing the VPN tunnel. A nil return means the server
// accepted the credentials.
func VerifyCredentials(ctx context.Context, opts Options) error {
	if strings.TrimSpace(opts.Username) == "" {
		return errors.New("username is empty")
	}
	if opts.Password == "" {
		return errors.New("password is empty")
	}
	client, err := newAuthClient(ctx, opts.ServerURL)
	if err != nil {
		return err
	}
	defer client.Close()
	client.Prof.Group = strings.TrimSpace(opts.Group)
	client.Prof.Username = strings.TrimSpace(opts.Username)
	client.Prof.Password = opts.Password
	if err := client.InitAuth(nil); err != nil {
		if groups := client.ServerGroups(); len(groups) != 0 && client.Prof.Group == "" {
			return fmt.Errorf("select one of the advertised user groups: %s", strings.Join(groups, ", "))
		}
		return err
	}
	if client.Conn != nil {
		_ = client.Conn.SetDeadline(time.Now().Add(opts.timeout()))
	}
	return client.PasswordAuth(&session.Session{})
}

func newAuthClient(ctx context.Context, serverURL string) (*acAuth.Client, error) {
	host, hostWithPort, groupAccess, err := serverParts(serverURL)
	if err != nil {
		return nil, err
	}
	info, err := osnet.GetLocalInterface(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect local interface: %w", err)
	}
	acBase.Setup()
	acBase.SetLogLevel("Error")
	profile := acAuth.Profile{
		Host: host, HostWithPort: hostWithPort, GroupAccess: groupAccess,
		Scheme: "https://",
	}
	local := acBase.Interface{Name: info.Name, Ip4: info.IP4, Mac: info.MAC, Gateway: info.Gateway}
	return acAuth.NewClient(profile, local), nil
}

func serverParts(raw string) (host, hostWithPort, groupAccess string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", errors.New("server URL is empty")
	}
	parseRaw := raw
	if !strings.Contains(parseRaw, "://") {
		parseRaw = "https://" + parseRaw
	}
	parsed, err := url.Parse(parseRaw)
	if err != nil || parsed.Host == "" {
		if err == nil {
			err = errors.New("missing host")
		}
		return "", "", "", fmt.Errorf("invalid server URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", "", "", errors.New("server URL must use https")
	}
	if parsed.User != nil {
		return "", "", "", errors.New("server URL must not contain user info")
	}
	host = parsed.Host
	if parsed.Port() == "" {
		hostWithPort = net.JoinHostPort(parsed.Hostname(), "443")
	} else {
		hostWithPort = parsed.Host
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	groupAccess = strings.TrimRight(parsed.String(), "/")
	return host, hostWithPort, groupAccess, nil
}
