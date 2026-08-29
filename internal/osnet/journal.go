package osnet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const networkJournalSchema = 1

type ownershipJournal struct {
	Schema                int      `json:"schema"`
	Phase                 string   `json:"phase"`
	InterfaceName         string   `json:"interface_name"`
	InterfaceIndex        int      `json:"interface_index,omitempty"`
	VPNAddress            string   `json:"vpn_address,omitempty"`
	Gateway               string   `json:"gateway,omitempty"`
	GatewayInterfaceIndex int      `json:"gateway_interface_index,omitempty"`
	ServerAddress         string   `json:"server_address,omitempty"`
	IncludeRoutes         []string `json:"include_routes,omitempty"`
	ExcludeRoutes         []string `json:"exclude_routes,omitempty"`
	DynamicInclude        []string `json:"dynamic_include,omitempty"`
	DynamicExclude        []string `json:"dynamic_exclude,omitempty"`
	DNSServers            []string `json:"dns_servers,omitempty"`
}

type journaledManager struct {
	mu      sync.Mutex
	inner   Manager
	path    string
	journal ownershipJournal
}

func wrapJournal(inner Manager, path, interfaceName string) Manager {
	if path == "" {
		return inner
	}
	return &journaledManager{inner: inner, path: path, journal: ownershipJournal{Schema: networkJournalSchema, Phase: "planned", InterfaceName: interfaceName}}
}

func (m *journaledManager) Up(ctx context.Context) error { return m.inner.Up(ctx) }

func (m *journaledManager) Set(ctx context.Context, cfg *Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg == nil || !cfg.VPNAddress.IsValid() {
		return m.closeLocked(ctx)
	}
	m.journal = journalFromConfig(cfg)
	m.journal.Phase = "planned"
	if err := writeNetworkJournal(m.path, m.journal); err != nil {
		return fmt.Errorf("write network ownership plan: %w", err)
	}
	if err := m.inner.Set(ctx, cfg); err != nil {
		cleanupErr := m.inner.Close(ctx)
		if cleanupErr == nil {
			_ = removeNetworkJournal(m.path)
			return err
		}
		return errors.Join(err, fmt.Errorf("rollback network transaction: %w", cleanupErr))
	}
	m.journal.Phase = "active"
	if err := writeNetworkJournal(m.path, m.journal); err != nil {
		cleanupErr := m.inner.Close(ctx)
		if cleanupErr == nil {
			_ = removeNetworkJournal(m.path)
		}
		return errors.Join(fmt.Errorf("commit network ownership journal: %w", err), cleanupErr)
	}
	return nil
}

func (m *journaledManager) SetDynamicRoutes(ctx context.Context, routes DynamicRoutes) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	nextInclude := AddrStrings(routes.Include)
	nextExclude := AddrStrings(routes.Exclude)
	planned := m.journal
	planned.DynamicInclude = unionStrings(m.journal.DynamicInclude, nextInclude)
	planned.DynamicExclude = unionStrings(m.journal.DynamicExclude, nextExclude)
	planned.Phase = "planned"
	if err := writeNetworkJournal(m.path, planned); err != nil {
		return fmt.Errorf("write dynamic route ownership plan: %w", err)
	}
	if err := m.inner.SetDynamicRoutes(ctx, routes); err != nil {
		m.journal = planned
		return err
	}
	committed := planned
	committed.DynamicInclude = nextInclude
	committed.DynamicExclude = nextExclude
	committed.Phase = "active"
	if err := writeNetworkJournal(m.path, committed); err != nil {
		m.journal = planned
		return err
	}
	m.journal = committed
	return nil
}

func unionStrings(first, second []string) []string {
	out := make([]string, 0, len(first)+len(second))
	seen := make(map[string]struct{}, len(first)+len(second))
	for _, values := range [][]string{first, second} {
		for _, value := range values {
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

func (m *journaledManager) Close(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeLocked(ctx)
}

func (m *journaledManager) closeLocked(ctx context.Context) error {
	if err := m.inner.Close(ctx); err != nil {
		m.journal.Phase = "cleanup_failed"
		_ = writeNetworkJournal(m.path, m.journal)
		return err
	}
	return removeNetworkJournal(m.path)
}

func journalFromConfig(cfg *Config) ownershipJournal {
	journal := ownershipJournal{
		Schema: networkJournalSchema, Phase: "planned", InterfaceName: cfg.InterfaceName, InterfaceIndex: cfg.InterfaceIndex,
		VPNAddress: cfg.VPNAddress.String(), GatewayInterfaceIndex: cfg.GatewayInterfaceIndex,
		IncludeRoutes: PrefixStrings(cfg.IncludeRoutes), ExcludeRoutes: PrefixStrings(cfg.ExcludeRoutes), DNSServers: AddrStrings(cfg.DNSServers),
	}
	if cfg.Gateway.IsValid() {
		journal.Gateway = cfg.Gateway.String()
	}
	if cfg.ServerAddress.IsValid() {
		journal.ServerAddress = cfg.ServerAddress.String()
	}
	return journal
}

// RecoverNetworkJournal restores an interrupted network transaction. It must
// run after elevation and before the daemon reports ready or opens its API.
func RecoverNetworkJournal(ctx context.Context, path string) error {
	journal, err := readNetworkJournal(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read network ownership journal: %w", err)
	}
	if err := platformRecoverJournal(ctx, journal); err != nil {
		return fmt.Errorf("recover network ownership journal: %w", err)
	}
	return removeNetworkJournal(path)
}

func readNetworkJournal(path string) (ownershipJournal, error) {
	var journal ownershipJournal
	b, err := os.ReadFile(path)
	if err != nil {
		return journal, err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return journal, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return journal, errors.New("network journal contains trailing data")
	}
	if journal.Schema != networkJournalSchema || journal.InterfaceName == "" {
		return journal, fmt.Errorf("unsupported or incomplete network journal schema %d", journal.Schema)
	}
	return journal, nil
}

func writeNetworkJournal(path string, journal ownershipJournal) error {
	if path == "" {
		return errors.New("network journal path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".flexconnect-network-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := replaceJournalFile(tmp, path); err != nil {
		return err
	}
	if err := secureJournalFile(path); err != nil {
		return err
	}
	return syncJournalDir(dir)
}

func removeNetworkJournal(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncJournalDir(filepath.Dir(path))
}
