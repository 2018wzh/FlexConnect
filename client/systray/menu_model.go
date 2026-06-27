package systray

import (
	"runtime"
	"sort"
	"strings"

	brandicons "flexconnect/assets/icons"
	"flexconnect/internal/types"
)

type menuAction int

const (
	menuActionNone menuAction = iota
	menuActionToggle
	menuActionProfile
	menuActionSocks5
	menuActionAutoReconnect
	menuActionApplyDNS
	menuActionCopyDiagnostics
	menuActionQuit
)

type trayIconColor string

const (
	trayIconBlue  trayIconColor = "blue"
	trayIconRed   trayIconColor = "red"
	trayIconGreen trayIconColor = "green"
)

type menuModel struct {
	Icon    trayIconColor
	Tooltip string
	Items   []menuItemModel
}

type menuItemModel struct {
	Title     string
	Tooltip   string
	Disabled  bool
	Checked   bool
	Checkbox  bool
	Separator bool
	Action    menuAction
	Toggle    toggleAction
	ProfileID string
	Value     bool
	Children  []menuItemModel
}

func buildMenuModel(status *types.Status, diag *types.Diagnostics, profiles []types.Profile) menuModel {
	profiles = append([]types.Profile(nil), profiles...)
	sort.Slice(profiles, func(i, j int) bool {
		return strings.ToLower(profileTitle(profiles[i])) < strings.ToLower(profileTitle(profiles[j]))
	})

	model := menuModel{Icon: trayIconColorForStatus(status), Tooltip: tooltipText(status, profiles)}
	model.Items = append(model.Items, statusItems(status, profiles)...)
	model.Items = append(model.Items, separatorItem())
	model.Items = append(model.Items, toggleItemForStatus(status))
	if info := informationItem(diag, status, profiles); len(info.Children) > 0 {
		model.Items = append(model.Items, info)
	}
	model.Items = append(model.Items, separatorItem(), profileMenuItem(status, profiles), separatorItem(), settingsMenuItem(status, profiles), separatorItem(), menuItemModel{
		Title:   "Quit",
		Tooltip: "Quit the app",
		Action:  menuActionQuit,
	})
	return model
}

func statusItems(status *types.Status, profiles []types.Profile) []menuItemModel {
	items := []menuItemModel{disabledItem("Status: " + stateText(status))}
	if status == nil {
		return items
	}
	if status.CurrentProfileID != "" {
		items = append(items, disabledItem("Current Profile: "+profileNameByID(profiles, status.CurrentProfileID)))
	}
	if status.ConnectedProfileID != "" && status.ConnectedProfileID != status.CurrentProfileID {
		items = append(items, disabledItem("Connected Profile: "+profileNameByID(profiles, status.ConnectedProfileID)))
	}
	if status.LastError != "" {
		items = append(items, disabledItem("Last Error: "+status.LastError))
	}
	return items
}

func toggleItemForStatus(status *types.Status) menuItemModel {
	toggle := toggleForStatus(status)
	item := menuItemModel{Title: toggle.Title, Disabled: !toggle.Enabled}
	if toggle.Enabled {
		item.Action = menuActionToggle
		item.Toggle = toggle.Action
	}
	return item
}

func informationItem(diag *types.Diagnostics, status *types.Status, profiles []types.Profile) menuItemModel {
	title := "Information"
	if status != nil && status.Session != nil && status.Session.VPNAddress != "" {
		title = "Information: " + status.Session.VPNAddress
	}
	rows := diagnosticsSummaryRows(diag, status, profiles)
	item := menuItemModel{Title: title}
	for _, row := range rows {
		if strings.TrimSpace(row) != "" {
			item.Children = append(item.Children, disabledItem(row))
		}
	}
	return item
}

func profileMenuItem(status *types.Status, profiles []types.Profile) menuItemModel {
	currentID := statusCurrentID(status)
	title := "Profiles"
	item := menuItemModel{Title: title}
	if len(profiles) == 0 {
		item.Children = append(item.Children, disabledItem("No profiles"))
		return item
	}
	for _, profile := range profiles {
		item.Children = append(item.Children, menuItemModel{
			Title:     profileTitle(profile),
			Tooltip:   profile.ServerURL,
			Checked:   profile.ID == currentID,
			Checkbox:  true,
			Action:    menuActionProfile,
			ProfileID: profile.ID,
		})
	}
	return item
}

func settingsMenuItem(status *types.Status, profiles []types.Profile) menuItemModel {
	currentProfile := currentProfileByID(profiles, statusCurrentID(status))
	socks5Enabled := status != nil && status.SOCKS5Enabled
	autoReconnectEnabled := false
	applyDNSEnabled := true
	if currentProfile != nil {
		autoReconnectEnabled = types.BoolValue(currentProfile.AutoReconnect, false)
		applyDNSEnabled = types.BoolValue(currentProfile.ApplyDNS, true)
	}
	disabled := currentProfile == nil
	return menuItemModel{
		Title: "Settings",
		Children: []menuItemModel{
			{Title: "Enable SOCKS5 Proxy", Disabled: disabled, Checked: socks5Enabled, Checkbox: true, Action: menuActionSocks5, Value: !socks5Enabled},
			{Title: "Auto Reconnect", Disabled: disabled, Checked: autoReconnectEnabled, Checkbox: true, Action: menuActionAutoReconnect, Value: !autoReconnectEnabled},
			{Title: "Apply DNS", Disabled: disabled, Checked: applyDNSEnabled, Checkbox: true, Action: menuActionApplyDNS, Value: !applyDNSEnabled},
			{Title: "Copy Diagnostics", Action: menuActionCopyDiagnostics},
		},
	}
}

func disabledItem(title string) menuItemModel {
	return menuItemModel{Title: title, Disabled: true}
}

func separatorItem() menuItemModel {
	return menuItemModel{Separator: true}
}

func trayIconColorForStatus(status *types.Status) trayIconColor {
	if status == nil {
		return trayIconBlue
	}
	switch status.State {
	case types.StateConnected:
		return trayIconGreen
	case types.StateConnecting, types.StateReconnecting, types.StateError:
		return trayIconRed
	default:
		return trayIconBlue
	}
}

func trayIconForColor(color trayIconColor) []byte {
	if runtime.GOOS == "linux" {
		switch color {
		case trayIconRed:
			return brandicons.TrayRedPNG()
		case trayIconGreen:
			return brandicons.TrayGreenPNG()
		default:
			return brandicons.TrayBluePNG()
		}
	}
	switch color {
	case trayIconRed:
		return brandicons.TrayRedICO()
	case trayIconGreen:
		return brandicons.TrayGreenICO()
	default:
		return brandicons.TrayBlueICO()
	}
}
