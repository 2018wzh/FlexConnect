package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flexconnect/internal/ipc"
	"flexconnect/internal/logging"
	"flexconnect/internal/secret"
	"flexconnect/internal/types"
)

var flagErrHelp = flag.ErrHelp

const (
	defaultSecretStore    = "keyring"
	defaultStartupProfile = "docker"
	defaultConnectTimeout = 2 * time.Minute
	keyringService        = "flexconnect"
	fileSecretName        = "secrets.json"
)

type envLookup func(string) (string, bool)

type startupConfig struct {
	ConnectOnStart bool
	Profile        types.Profile
	Password       string
	ConnectTimeout time.Duration
	RequireSOCKS5  bool
}

type startupDaemon interface {
	ListProfiles() []types.Profile
	CreateProfile(types.Profile, string) (types.Profile, error)
	UpdateProfile(string, types.ProfileUpdateRequest) (types.Profile, error)
	SwitchProfile(context.Context, string) error
	Connect(context.Context, string) error
	Status() types.Status
}

func parseDaemonOptions(args []string, lookup envLookup) (daemonOptions, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	verbose, err := envBoolDefault(lookup, "FLEXCONNECT_VERBOSE", false)
	if err != nil {
		return daemonOptions{}, err
	}
	secretStore := envStringDefault(lookup, "FLEXCONNECT_SECRET_STORE", defaultSecretStore)
	if err := validateSecretStore(secretStore); err != nil {
		return daemonOptions{}, err
	}
	startup, err := parseStartupConfig(lookup)
	if err != nil {
		return daemonOptions{}, err
	}

	opts := daemonOptions{
		socket:      envStringDefault(lookup, "FLEXCONNECT_SOCKET", ipc.DefaultSocketPath()),
		state:       envStringDefault(lookup, "FLEXCONNECT_STATE", defaultStatePath()),
		verbose:     verbose,
		secretStore: secretStore,
		startup:     startup,
	}
	fs := flag.NewFlagSet("flexconnectd", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.socket, "socket", opts.socket, "daemon socket or named pipe path")
	fs.StringVar(&opts.state, "state", opts.state, "path to state file")
	fs.BoolVar(&opts.verbose, "v", opts.verbose, "enable verbose debug logging")
	fs.BoolVar(&opts.verbose, "verbose", opts.verbose, "same as -v")
	if err := fs.Parse(args); err != nil {
		return daemonOptions{}, err
	}
	if fs.NArg() != 0 {
		return daemonOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return opts, nil
}

func parseStartupConfig(lookup envLookup) (*startupConfig, error) {
	connectOnStart, err := envBoolDefault(lookup, "FLEXCONNECT_CONNECT_ON_START", false)
	if err != nil {
		return nil, err
	}
	if !connectOnStart {
		return nil, nil
	}

	password, err := readStartupPassword(lookup)
	if err != nil {
		return nil, err
	}
	profile := types.NewProfile(envStringDefault(lookup, "FLEXCONNECT_PROFILE_NAME", defaultStartupProfile))
	profile.ID = envStringDefault(lookup, "FLEXCONNECT_PROFILE_ID", defaultStartupProfile)
	profile.SecretRef = "profile/" + profile.ID
	profile.ServerURL = envStringDefault(lookup, "FLEXCONNECT_SERVER", "")
	profile.Username = envStringDefault(lookup, "FLEXCONNECT_USERNAME", "")
	profile.Group = envStringDefault(lookup, "FLEXCONNECT_GROUP", "")
	profile.CustomInclude = envCSV(lookup, "FLEXCONNECT_INCLUDE_ROUTES")
	profile.CustomExclude = envCSV(lookup, "FLEXCONNECT_EXCLUDE_ROUTES")
	profile.DNSOverrides = envCSV(lookup, "FLEXCONNECT_DNS")
	if v, set, err := envBoolOptional(lookup, "FLEXCONNECT_ACCEPT_SERVER_ROUTES"); err != nil {
		return nil, err
	} else if set {
		profile.AcceptServerRoutes = v
	}
	if v, set, err := envBoolOptional(lookup, "FLEXCONNECT_AUTO_RECONNECT"); err != nil {
		return nil, err
	} else if set {
		profile.AutoReconnect = types.BoolPtr(v)
	}
	if v, set, err := envBoolOptional(lookup, "FLEXCONNECT_APPLY_DNS"); err != nil {
		return nil, err
	} else if set {
		profile.ApplyDNS = types.BoolPtr(v)
	}
	if v, set, err := envIntOptional(lookup, "FLEXCONNECT_MTU"); err != nil {
		return nil, err
	} else if set {
		profile.MTU = v
	}
	if v, set, err := envBoolOptional(lookup, "FLEXCONNECT_SOCKS5_ENABLED"); err != nil {
		return nil, err
	} else if set {
		profile.SOCKS5Enabled = v
	}
	if v, ok := lookup("FLEXCONNECT_SOCKS5_LISTEN"); ok {
		profile.SOCKS5Listen = strings.TrimSpace(v)
	}
	timeout, err := envDurationDefault(lookup, "FLEXCONNECT_CONNECT_TIMEOUT", defaultConnectTimeout)
	if err != nil {
		return nil, err
	}

	var missing []string
	if strings.TrimSpace(profile.ServerURL) == "" {
		missing = append(missing, "FLEXCONNECT_SERVER")
	}
	if strings.TrimSpace(profile.Username) == "" {
		missing = append(missing, "FLEXCONNECT_USERNAME")
	}
	if password == "" {
		missing = append(missing, "FLEXCONNECT_PASSWORD or FLEXCONNECT_PASSWORD_FILE")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("FLEXCONNECT_CONNECT_ON_START requires %s", strings.Join(missing, ", "))
	}

	return &startupConfig{
		ConnectOnStart: true,
		Profile:        profile,
		Password:       password,
		ConnectTimeout: timeout,
		RequireSOCKS5:  profile.SOCKS5Enabled,
	}, nil
}

func readStartupPassword(lookup envLookup) (string, error) {
	password, passwordSet := lookup("FLEXCONNECT_PASSWORD")
	passwordFile, passwordFileSet := lookup("FLEXCONNECT_PASSWORD_FILE")
	if passwordSet && passwordFileSet {
		return "", errors.New("set only one of FLEXCONNECT_PASSWORD or FLEXCONNECT_PASSWORD_FILE")
	}
	if passwordSet {
		return password, nil
	}
	if !passwordFileSet {
		return "", nil
	}
	path := strings.TrimSpace(passwordFile)
	if path == "" {
		return "", errors.New("FLEXCONNECT_PASSWORD_FILE is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read FLEXCONNECT_PASSWORD_FILE: %w", err)
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func bootstrapStartup(ctx context.Context, daemon startupDaemon, cfg *startupConfig) error {
	if cfg == nil || !cfg.ConnectOnStart {
		return nil
	}
	profile := cfg.Profile
	if profile.ID == "" {
		return errors.New("startup profile id is required")
	}
	if profile.SecretRef == "" {
		profile.SecretRef = "profile/" + profile.ID
	}
	existing := false
	for _, p := range daemon.ListProfiles() {
		if p.ID == profile.ID {
			existing = true
			break
		}
	}
	if existing {
		password := cfg.Password
		req := types.ProfileUpdateRequest{
			Name:               &profile.Name,
			ServerURL:          &profile.ServerURL,
			Username:           &profile.Username,
			Group:              &profile.Group,
			AcceptServerRoutes: &profile.AcceptServerRoutes,
			AutoReconnect:      profile.AutoReconnect,
			ApplyDNS:           profile.ApplyDNS,
			CustomInclude:      append([]string(nil), profile.CustomInclude...),
			CustomExclude:      append([]string(nil), profile.CustomExclude...),
			DNSOverrides:       append([]string(nil), profile.DNSOverrides...),
			SOCKS5Enabled:      &profile.SOCKS5Enabled,
			SOCKS5Listen:       &profile.SOCKS5Listen,
			MTU:                &profile.MTU,
			Password:           &password,
		}
		if _, err := daemon.UpdateProfile(profile.ID, req); err != nil {
			return fmt.Errorf("startup update profile %q: %w", profile.ID, err)
		}
	} else {
		if _, err := daemon.CreateProfile(profile, cfg.Password); err != nil {
			return fmt.Errorf("startup create profile %q: %w", profile.ID, err)
		}
	}
	if err := daemon.SwitchProfile(ctx, profile.ID); err != nil {
		return fmt.Errorf("startup switch profile %q: %w", profile.ID, err)
	}
	connectCtx := ctx
	cancel := func() {}
	if cfg.ConnectTimeout > 0 {
		connectCtx, cancel = context.WithTimeout(ctx, cfg.ConnectTimeout)
	}
	defer cancel()
	if err := daemon.Connect(connectCtx, profile.ID); err != nil {
		return fmt.Errorf("startup connect profile %q: %w", profile.ID, err)
	}
	if cfg.RequireSOCKS5 {
		status := daemon.Status()
		if !status.SOCKS5Enabled || status.SOCKS5Listen == "" {
			return fmt.Errorf("startup required SOCKS5 listener is not active")
		}
	}
	return nil
}

// fileSecretPath derives the fallback secret file from the daemon state path,
// so the secrets live next to state.json (for example
// /var/lib/flexconnect/secrets.json).
func fileSecretPath(statePath string) string {
	if strings.TrimSpace(statePath) == "" {
		statePath = defaultStatePath()
	}
	return filepath.Join(filepath.Dir(statePath), fileSecretName)
}

// newSecretStore builds the secret.Store selected by kind. The default
// "keyring" mode probes the OS keyring and automatically falls back to a file
// secret store when no keyring is available, so the daemon still starts on
// headless hosts.
func newSecretStore(statePath, kind string) (secret.Store, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "keyring":
		ks := secret.NewKeyringStore(keyringService)
		if err := ks.Probe(); err != nil {
			path := fileSecretPath(statePath)
			logging.WithComponent("flexconnectd").Warnf(
				"OS keyring unavailable (%v); falling back to file secret store at %s", err, path)
			return secret.NewFileStore(path), nil
		}
		return ks, nil
	case "file":
		return secret.NewFileStore(fileSecretPath(statePath)), nil
	case "memory":
		return secret.NewMemoryStore(), nil
	default:
		return nil, fmt.Errorf("invalid FLEXCONNECT_SECRET_STORE %q: expected keyring, memory, or file", kind)
	}
}

func validateSecretStore(kind string) error {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "keyring", "memory", "file":
		return nil
	default:
		return fmt.Errorf("invalid FLEXCONNECT_SECRET_STORE %q: expected keyring, memory, or file", kind)
	}
}

func envStringDefault(lookup envLookup, key, fallback string) string {
	if v, ok := lookup(key); ok {
		return strings.TrimSpace(v)
	}
	return fallback
}

func envBoolDefault(lookup envLookup, key string, fallback bool) (bool, error) {
	v, set, err := envBoolOptional(lookup, key)
	if err != nil || !set {
		return fallback, err
	}
	return v, nil
}

func envBoolOptional(lookup envLookup, key string) (bool, bool, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return false, false, nil
	}
	v, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, true, fmt.Errorf("invalid %s: %w", key, err)
	}
	return v, true, nil
}

func envIntOptional(lookup envLookup, key string) (int, bool, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return 0, false, nil
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, true, fmt.Errorf("invalid %s: %w", key, err)
	}
	return v, true, nil
}

func envDurationDefault(lookup envLookup, key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	v, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("invalid %s: must be positive", key)
	}
	return v, nil
}

func envCSV(lookup envLookup, key string) []string {
	raw, ok := lookup(key)
	if !ok {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
