package systray

import (
	"fmt"
	"strings"
	"testing"

	"flexconnect/internal/types"
)

type recordingNotifier struct {
	calls  []string
	alerts []string
}

func (n *recordingNotifier) Send(title, body string) error {
	n.calls = append(n.calls, title+"|"+body)
	return nil
}

func (n *recordingNotifier) Alert(title, body string) error {
	n.alerts = append(n.alerts, title+"|"+body)
	return nil
}

func TestConnectionNotificationsDeduplicateLifecycleKind(t *testing.T) {
	recorder := &recordingNotifier{}
	menu := &Menu{rebuildCh: make(chan struct{}, 1), notifier: recorder}
	event := types.ConnectionEvent{ID: "event-1", ConnectionID: "reconnect-1", Kind: "reconnect_scheduled", Attempt: 1}
	menu.handleNotify(types.Notify{Connection: &event}, func() {})
	menu.handleNotify(types.Notify{Connection: &event}, func() {})
	if len(recorder.calls) != 1 {
		t.Fatalf("notification calls = %d, want 1", len(recorder.calls))
	}
}

func TestTrayErrorsAlertAndDeduplicate(t *testing.T) {
	recorder := &recordingNotifier{}
	menu := &Menu{notifier: recorder}
	err := fmt.Errorf("daemon unavailable")
	menu.reportError("daemon unavailable", err)
	menu.reportError("daemon unavailable", err)
	if len(recorder.alerts) != 1 {
		t.Fatalf("alert calls = %d, want 1", len(recorder.alerts))
	}
	if !strings.Contains(recorder.alerts[0], "daemon unavailable") {
		t.Fatalf("alert = %q, want operation and error", recorder.alerts[0])
	}
}

func TestConnectionNotificationKinds(t *testing.T) {
	recorder := &recordingNotifier{}
	menu := &Menu{rebuildCh: make(chan struct{}, 1), notifier: recorder}
	for _, kind := range []string{"connection_lost", "reconnect_scheduled", "reconnected", "reconnect_failed"} {
		event := types.ConnectionEvent{ID: kind, ConnectionID: kind, Kind: kind, ReasonCode: "tls_read_error", Attempt: 2}
		menu.handleNotify(types.Notify{Connection: &event}, func() {})
	}
	if len(recorder.calls) != 4 {
		t.Fatalf("notification calls = %d, want 4", len(recorder.calls))
	}
}
