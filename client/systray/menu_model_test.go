package systray

import (
	"strings"
	"testing"

	"flexconnect/internal/types"
)

func TestMenuModelUnavailableHasCoreItems(t *testing.T) {
	model := buildMenuModel(nil, types.TrafficSnapshot{}, nil)

	assertHasItem(t, model.Items, "Status: Unavailable")
	connect := assertHasItem(t, model.Items, "Connect")
	if connect.Disabled || connect.Action != menuActionToggle || connect.Toggle != toggleConnect {
		t.Fatalf("Connect item = %+v", connect)
	}
	profile := assertHasItem(t, model.Items, "Profiles")
	assertHasItem(t, profile.Children, "No profiles")
	assertHasItem(t, model.Items, "Settings")
	assertHasItem(t, model.Items, "Quit")
}

func TestMenuModelConnectingDisablesMainAction(t *testing.T) {
	model := buildMenuModel(&types.Status{State: types.StateConnecting, CurrentProfileID: "p1"}, types.TrafficSnapshot{}, []types.Profile{
		{ID: "p1", Name: "Work"},
	})

	item := assertHasItem(t, model.Items, "Connecting")
	if !item.Disabled || item.Action != menuActionNone {
		t.Fatalf("Connecting item = %+v", item)
	}
	assertHasItem(t, model.Items, "Profiles")
	assertHasItem(t, model.Items, "Settings")
	assertHasItem(t, model.Items, "Quit")
}

func TestMenuModelConnectedShowsDisconnectAndVPNIP(t *testing.T) {
	status := &types.Status{
		State:              types.StateConnected,
		CurrentProfileID:   "p1",
		ConnectedProfileID: "p1",
		Session:            &types.SessionInfo{VPNAddress: "10.0.0.8"},
	}
	traffic := types.TrafficSnapshot{
		Connected:              true,
		BytesSent:              1024,
		BytesReceived:          2048,
		BytesSentPerSecond:     512,
		BytesReceivedPerSecond: 1536,
	}
	model := buildMenuModel(status, traffic, []types.Profile{{ID: "p1", Name: "Work"}})

	disconnect := assertHasItem(t, model.Items, "Disconnect")
	if disconnect.Disabled || disconnect.Action != menuActionToggle || disconnect.Toggle != toggleDisconnect {
		t.Fatalf("Disconnect item = %+v", disconnect)
	}
	wantTooltip := strings.Join([]string{
		"FlexConnect: Connected",
		"Work · 10.0.0.8",
		"Traffic ↑1.00 KiB ↓2.00 KiB",
		"Speed ↑512 B/s ↓1.50 KiB/s",
	}, "\n")
	if model.Tooltip != wantTooltip {
		t.Fatalf("tooltip = %q, want %q", model.Tooltip, wantTooltip)
	}
	info := assertHasItem(t, model.Items, "Information: 10.0.0.8")
	assertHasItem(t, info.Children, "FlexConnect: Connected")
	assertHasItem(t, info.Children, "Work · 10.0.0.8")
	assertHasItem(t, info.Children, "Traffic ↑1.00 KiB ↓2.00 KiB")
	assertHasItem(t, info.Children, "Speed ↑512 B/s ↓1.50 KiB/s")
	profile := assertHasItem(t, model.Items, "Profiles")
	child := assertHasItem(t, profile.Children, "Work")
	if !child.Checked {
		t.Fatalf("current profile not checked: %+v", child)
	}
	if countItemsWithPrefix(model.Items, "Current Profile: Work")+countItemsWithPrefix(model.Items, "Connected Profile: Work") != 1 {
		t.Fatalf("duplicate current/connected profile rows: %+v", itemTitles(model.Items))
	}
}

func TestMenuModelErrorShowsLastError(t *testing.T) {
	model := buildMenuModel(&types.Status{State: types.StateError, LastError: "bad password"}, types.TrafficSnapshot{}, nil)

	assertHasItem(t, model.Items, "Last Error: bad password")
	if trayIconColorForStatus(&types.Status{State: types.StateError}) != trayIconRed {
		t.Fatal("error state should use red tray icon")
	}
}

func TestTrayIconColorForStatus(t *testing.T) {
	tests := []struct {
		name   string
		status *types.Status
		want   trayIconColor
	}{
		{name: "nil", status: nil, want: trayIconBlue},
		{name: "disconnected", status: &types.Status{State: types.StateDisconnected}, want: trayIconBlue},
		{name: "connecting", status: &types.Status{State: types.StateConnecting}, want: trayIconRed},
		{name: "reconnecting", status: &types.Status{State: types.StateReconnecting}, want: trayIconRed},
		{name: "error", status: &types.Status{State: types.StateError}, want: trayIconRed},
		{name: "connected", status: &types.Status{State: types.StateConnected}, want: trayIconGreen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trayIconColorForStatus(tt.status); got != tt.want {
				t.Fatalf("color = %s, want %s", got, tt.want)
			}
		})
	}
}

func assertHasItem(t *testing.T, items []menuItemModel, title string) menuItemModel {
	t.Helper()
	for _, item := range items {
		if item.Title == title {
			return item
		}
	}
	t.Fatalf("missing item %q in %v", title, itemTitles(items))
	return menuItemModel{}
}

func itemTitles(items []menuItemModel) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item.Separator {
			out = append(out, "-")
			continue
		}
		out = append(out, item.Title)
	}
	return out
}

func countItemsWithPrefix(items []menuItemModel, prefix string) int {
	count := 0
	for _, item := range items {
		if strings.HasPrefix(item.Title, prefix) {
			count++
		}
	}
	return count
}
