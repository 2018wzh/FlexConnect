package systray

import (
	"context"
	"testing"
	"time"

	systraylib "fyne.io/systray"

	"flexconnect/internal/types"
)

func TestOnClickIgnoresClosedClickedChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	item := &systraylib.MenuItem{ClickedCh: make(chan struct{})}
	called := make(chan struct{}, 1)

	onClick(ctx, item, func(context.Context) {
		called <- struct{}{}
	})
	close(item.ClickedCh)

	select {
	case <-called:
		t.Fatal("closed ClickedCh triggered action")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestOnClickRunsActionOnClick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	item := &systraylib.MenuItem{ClickedCh: make(chan struct{}, 1)}
	called := make(chan struct{}, 1)

	onClick(ctx, item, func(context.Context) {
		called <- struct{}{}
	})
	item.ClickedCh <- struct{}{}

	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("click did not trigger action")
	}
}

func TestHandleNotifyDoesNotRebuildOnTrafficOnly(t *testing.T) {
	menu := &Menu{rebuildCh: make(chan struct{}, 1)}
	tooltipUpdates := 0

	menu.handleNotify(types.Notify{Traffic: &types.TrafficSnapshot{BytesSent: 1}}, func() {
		tooltipUpdates++
	})

	if tooltipUpdates != 1 {
		t.Fatalf("tooltip updates = %d, want 1", tooltipUpdates)
	}
	select {
	case <-menu.rebuildCh:
		t.Fatal("traffic-only notify queued a menu rebuild")
	default:
	}
}

func TestHandleNotifyRebuildsOnStateChange(t *testing.T) {
	menu := &Menu{rebuildCh: make(chan struct{}, 1)}

	menu.handleNotify(types.Notify{Status: &types.Status{State: types.StateConnected}}, func() {})

	select {
	case <-menu.rebuildCh:
	default:
		t.Fatal("state notify did not queue a menu rebuild")
	}
}
