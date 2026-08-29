package osnet

import (
	"errors"
	"testing"
)

func TestDNSBackendSelectionFallsThroughAndFailsClosed(t *testing.T) {
	firstErr := errors.New("resolved unavailable")
	called := 0
	name, err := chooseDNSBackend([]dnsBackendAttempt{
		{name: "systemd-resolved", set: func() error { called++; return firstErr }},
		{name: "network-manager", set: func() error { called++; return nil }},
		{name: "resolvconf", set: func() error { t.Fatal("selection continued after success"); return nil }},
	})
	if err != nil || name != "network-manager" || called != 2 {
		t.Fatalf("selection = name %q err %v called %d", name, err, called)
	}
	name, err = chooseDNSBackend([]dnsBackendAttempt{{name: "systemd-resolved", set: func() error { return firstErr }}})
	if name != "" || !errors.Is(err, firstErr) {
		t.Fatalf("failed selection = name %q err %v", name, err)
	}
}
