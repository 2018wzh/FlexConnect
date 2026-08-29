package osnet

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

type journalTestManager struct {
	setErr     error
	dynamicErr error
	closeErr   error
	closed     int
}

func (*journalTestManager) Up(context.Context) error             { return nil }
func (m *journalTestManager) Set(context.Context, *Config) error { return m.setErr }
func (m *journalTestManager) SetDynamicRoutes(context.Context, DynamicRoutes) error {
	return m.dynamicErr
}
func (m *journalTestManager) Close(context.Context) error {
	m.closed++
	return m.closeErr
}

func TestDynamicRouteFailureJournalRetainsOldAndPlannedOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network-ownership.json")
	inner := &journalTestManager{}
	manager := wrapJournal(inner, path, "test0")
	cfg := &Config{InterfaceName: "test0", VPNAddress: netip.MustParsePrefix("10.0.0.1/32")}
	if err := manager.Set(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDynamicRoutes(context.Background(), DynamicRoutes{Include: []netip.Addr{netip.MustParseAddr("10.1.1.1")}}); err != nil {
		t.Fatal(err)
	}
	inner.dynamicErr = errors.New("route reconcile failed")
	if err := manager.SetDynamicRoutes(context.Background(), DynamicRoutes{Include: []netip.Addr{netip.MustParseAddr("10.2.2.2")}}); !errors.Is(err, inner.dynamicErr) {
		t.Fatalf("dynamic route error = %v", err)
	}
	journal, err := readNetworkJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != "planned" || len(journal.DynamicInclude) != 2 {
		t.Fatalf("failure journal = %+v", journal)
	}
}

func TestJournaledManagerCommitsAndClearsOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network-ownership.json")
	inner := &journalTestManager{}
	manager := wrapJournal(inner, path, "test0")
	cfg := &Config{InterfaceName: "test0", VPNAddress: netip.MustParsePrefix("10.0.0.1/32"), IncludeRoutes: []netip.Prefix{netip.MustParsePrefix("10.10.0.0/16")}}
	if err := manager.Set(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	journal, err := readNetworkJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != "active" || len(journal.IncludeRoutes) != 1 {
		t.Fatalf("journal = %+v", journal)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal survived successful cleanup: %v", err)
	}
}

func TestJournaledManagerPreservesJournalWhenRollbackFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network-ownership.json")
	inner := &journalTestManager{setErr: errors.New("set failed"), closeErr: errors.New("cleanup failed")}
	manager := wrapJournal(inner, path, "test0")
	err := manager.Set(context.Background(), &Config{InterfaceName: "test0", VPNAddress: netip.MustParsePrefix("10.0.0.1/32")})
	if err == nil || !errors.Is(err, inner.setErr) || !errors.Is(err, inner.closeErr) {
		t.Fatalf("Set error = %v", err)
	}
	if _, err := readNetworkJournal(path); err != nil {
		t.Fatalf("cleanup journal missing: %v", err)
	}
}

func TestRecoverNetworkJournalRejectsCorruptionWithoutModification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network-ownership.json")
	if err := os.WriteFile(path, []byte(`{"schema":1,"interface_name":"test0","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecoverNetworkJournal(context.Background(), path); err == nil {
		t.Fatal("corrupt journal was accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("corrupt journal was modified: %v", err)
	}
}
