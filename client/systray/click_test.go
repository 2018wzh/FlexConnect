package systray

import (
	"context"
	"testing"
	"time"

	systraylib "fyne.io/systray"
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
