package systray

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"flexconnect/internal/types"
)

// fakeUpdateClient embeds the fully-failing client and overrides UpdateCheck so
// update-loop tests can inject arbitrary results without a live daemon.
type fakeUpdateClient struct {
	fakeFailingClient
	update *types.UpdateInfo
	err    error
	calls  int
}

func (f *fakeUpdateClient) UpdateCheck(context.Context) (*types.UpdateInfo, error) {
	f.calls++
	return f.update, f.err
}

func TestUpdateCheckInterval(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want time.Duration
	}{
		{name: "empty defaults to 6h", env: "", want: defaultUpdateCheckInterval},
		{name: "disabled", env: "disabled", want: 0},
		{name: "zero", env: "0", want: 0},
		{name: "custom", env: "30m", want: 30 * time.Minute},
		{name: "invalid", env: "notaduration", want: 0},
		{name: "negative", env: "-5m", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FLEXCONNECT_UPDATE_INTERVAL", tt.env)
			if got := updateCheckInterval(); got != tt.want {
				t.Fatalf("updateCheckInterval() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFetchUpdateInfoNotifiesOncePerVersion(t *testing.T) {
	recorder := &recordingNotifier{}
	client := &fakeUpdateClient{update: &types.UpdateInfo{
		LatestVersion:   "9.9.9",
		UpdateAvailable: true,
		ReleaseURL:      "https://github.com/owner/repo/releases/tag/v9.9.9",
	}}
	menu := &Menu{Client: client, rebuildCh: make(chan struct{}, 1), notifier: recorder}

	menu.fetchUpdateInfo(context.Background())
	if len(recorder.calls) != 1 {
		t.Fatalf("notification calls = %d, want 1", len(recorder.calls))
	}
	if !strings.Contains(recorder.calls[0], "9.9.9") {
		t.Fatalf("notification = %q, want it to mention the new version", recorder.calls[0])
	}

	// Same version again: no new notification.
	menu.fetchUpdateInfo(context.Background())
	if len(recorder.calls) != 1 {
		t.Fatalf("notification calls after repeat = %d, want 1", len(recorder.calls))
	}

	// A different (newer) release: notify again.
	client.update = &types.UpdateInfo{LatestVersion: "10.0.0", UpdateAvailable: true}
	menu.fetchUpdateInfo(context.Background())
	if len(recorder.calls) != 2 {
		t.Fatalf("notification calls after new version = %d, want 2", len(recorder.calls))
	}
}

func TestFetchUpdateInfoNoNotificationWhenUpToDate(t *testing.T) {
	recorder := &recordingNotifier{}
	client := &fakeUpdateClient{update: &types.UpdateInfo{LatestVersion: "1.0.6", UpdateAvailable: false}}
	menu := &Menu{Client: client, rebuildCh: make(chan struct{}, 1), notifier: recorder}

	menu.fetchUpdateInfo(context.Background())
	if len(recorder.calls) != 0 {
		t.Fatalf("notification calls = %d, want 0", len(recorder.calls))
	}
}

func TestFetchUpdateInfoIgnoresErrors(t *testing.T) {
	recorder := &recordingNotifier{}
	client := &fakeUpdateClient{err: errors.New("boom")}
	menu := &Menu{Client: client, rebuildCh: make(chan struct{}, 1), notifier: recorder}

	menu.fetchUpdateInfo(context.Background())
	if menu.updateInfo != nil {
		t.Fatal("updateInfo should remain nil when the check fails")
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("notification calls = %d, want 0", len(recorder.calls))
	}
}

func TestUpdateCheckLoopDisabledExitsImmediately(t *testing.T) {
	t.Setenv("FLEXCONNECT_UPDATE_INTERVAL", "disabled")
	client := &fakeUpdateClient{update: &types.UpdateInfo{UpdateAvailable: true}}
	menu := &Menu{Client: client, rebuildCh: make(chan struct{}, 1)}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { menu.updateCheckLoop(ctx); close(done) }()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("updateCheckLoop should return immediately when disabled")
	}
	if client.calls != 0 {
		t.Fatalf("UpdateCheck calls = %d, want 0 when disabled", client.calls)
	}
}
