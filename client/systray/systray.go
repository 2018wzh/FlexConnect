package systray

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	systraylib "fyne.io/systray"

	brandicons "flexconnect/assets/icons"
	"flexconnect/client/local"
	"flexconnect/internal/logging"
	"flexconnect/internal/types"
)

var systrayLog = logging.WithComponent("flexconnect-systray")

type toggleAction int

const (
	toggleNone toggleAction = iota
	toggleConnect
	toggleDisconnect
)

type toggleState struct {
	Title   string
	Enabled bool
	Action  toggleAction
}

type Menu struct {
	Client *local.Client

	mu          sync.Mutex
	status      *types.Status
	diag        *types.Diagnostics
	profiles    []types.Profile
	rebuildCh   chan struct{}
	eventCancel context.CancelFunc
}

func (m *Menu) Run() {
	if m.Client == nil {
		m.Client = &local.Client{}
	}
	systraylib.SetTitle("FlexConnect")
	systraylib.Run(m.onReady, m.onExit)
}

func (m *Menu) onReady() {
	setTrayIcon()
	setTooltip("FlexConnect")
	m.init()
	m.refresh(context.Background())
	m.rebuild()
	go m.watch()
}

func (m *Menu) onExit() {
	m.mu.Lock()
	if m.eventCancel != nil {
		m.eventCancel()
	}
	m.mu.Unlock()
}

func (m *Menu) init() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rebuildCh == nil {
		m.rebuildCh = make(chan struct{}, 1)
	}
}

func (m *Menu) requestRebuild() {
	m.init()
	select {
	case m.rebuildCh <- struct{}{}:
	default:
	}
}

func (m *Menu) refresh(ctx context.Context) {
	status, err := m.Client.Status(ctx)
	if err != nil {
		systrayLog.Error(err)
	}
	profiles, err := m.Client.Profiles(ctx)
	if err != nil {
		systrayLog.Error(err)
	}
	diag, err := m.Client.Diagnostics(ctx)
	if err != nil {
		systrayLog.Error(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if status != nil {
		m.status = status
	}
	if profiles != nil {
		m.profiles = profiles
	}
	if diag != nil {
		m.diag = diag
		if m.status == nil {
			statusCopy := diag.Status
			m.status = &statusCopy
		}
		if len(m.profiles) == 0 {
			m.profiles = append([]types.Profile(nil), diag.Profiles...)
		}
	}
}

func (m *Menu) rebuild() {
	m.mu.Lock()
	status := copyStatus(m.status)
	diag := copyDiagnostics(m.diag)
	profiles := append([]types.Profile(nil), m.profiles...)
	if m.eventCancel != nil {
		m.eventCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.eventCancel = cancel
	m.mu.Unlock()

	sort.Slice(profiles, func(i, j int) bool {
		return strings.ToLower(profileTitle(profiles[i])) < strings.ToLower(profileTitle(profiles[j]))
	})
	systraylib.ResetMenu()
	setTrayIcon()
	setTooltip(tooltipText(status, profiles))

	m.addStatusSection(status, profiles)
	systraylib.AddSeparator()

	toggle := toggleForStatus(status)
	toggleItem := systraylib.AddMenuItem(toggle.Title, "")
	if toggle.Enabled {
		onClick(ctx, toggleItem, func(context.Context) {
			m.handleToggle(toggle.Action)
		})
	} else {
		toggleItem.Disable()
	}
	m.addInformationSection(diag, status, profiles)
	systraylib.AddSeparator()

	m.addProfileSection(ctx, status, profiles)
	systraylib.AddSeparator()

	m.addSettingsSection(ctx, status, profiles)
}

func (m *Menu) addStatusSection(status *types.Status, profiles []types.Profile) {
	addDisabled("Status: " + stateText(status))
	if status == nil {
		return
	}
	if status.CurrentProfileID != "" {
		addDisabled("Current Profile: " + profileNameByID(profiles, status.CurrentProfileID))
	}
	if status.ConnectedProfileID != "" {
		addDisabled("Connected Profile: " + profileNameByID(profiles, status.ConnectedProfileID))
	}
	if status.LastError != "" {
		addDisabled("Last Error: " + status.LastError)
	}
}

func (m *Menu) addInformationSection(diag *types.Diagnostics, status *types.Status, profiles []types.Profile) {
	infoTitle := "Information"
	if status != nil && status.Session != nil && status.Session.VPNAddress != "" {
		infoTitle = "Information: " + status.Session.VPNAddress
	}
	info := systraylib.AddMenuItem(infoTitle, "")
	for _, row := range diagnosticsSummaryRows(diag, status, profiles) {
		info.AddSubMenuItem(row, "").Disable()
	}
}

func (m *Menu) addProfileSection(ctx context.Context, status *types.Status, profiles []types.Profile) {
	currentID := ""
	if status != nil {
		currentID = status.CurrentProfileID
	}
	title := "Profile"
	if currentID != "" {
		title = "Profile: " + profileNameByID(profiles, currentID)
	}
	profileMenu := systraylib.AddMenuItem(title, "")
	if len(profiles) == 0 {
		profileMenu.AddSubMenuItem("No profiles", "").Disable()
		return
	}
	for _, profile := range profiles {
		profile := profile
		item := profileMenu.AddSubMenuItemCheckbox(profileTitle(profile), profile.ServerURL, profile.ID == currentID)
		if profile.ID == currentID {
			item.Check()
		}
		onClick(ctx, item, func(context.Context) {
			m.handleProfileSelection(ctx, profile.ID)
		})
	}
}

func (m *Menu) handleProfileSelection(ctx context.Context, profileID string) {
	m.mu.Lock()
	status := copyStatus(m.status)
	m.mu.Unlock()
	if status != nil && status.CurrentProfileID == profileID {
		return
	}

	if status != nil && status.State == types.StateConnected {
		if err := m.Client.Disconnect(ctx); err != nil {
			systrayLog.Error(err)
			return
		}
	}

	if err := m.Client.SwitchProfile(ctx, profileID); err != nil {
		systrayLog.Error(err)
		return
	}
	if status != nil && status.State == types.StateConnected {
		if err := m.Client.Connect(ctx, profileID); err != nil {
			systrayLog.Error(err)
			return
		}
	}
	m.refresh(ctx)
	m.requestRebuild()
}

func (m *Menu) addSettingsSection(ctx context.Context, status *types.Status, profiles []types.Profile) {
	settings := systraylib.AddMenuItem("Settings", "")
	socks5Enabled := false
	if status != nil {
		socks5Enabled = status.SOCKS5Enabled
	}
	currentProfile := currentProfileByID(profiles, statusCurrentID(status))
	autoReconnectEnabled := false
	applyDNSEnabled := true
	if currentProfile != nil {
		autoReconnectEnabled = types.BoolValue(currentProfile.AutoReconnect, false)
		applyDNSEnabled = types.BoolValue(currentProfile.ApplyDNS, true)
	}
	socks5Item := settings.AddSubMenuItemCheckbox("Enable SOCKS5 Proxy", "", socks5Enabled)
	if status == nil || status.CurrentProfileID == "" {
		socks5Item.Disable()
	} else {
		onClick(ctx, socks5Item, func(context.Context) { m.handleSocks5Toggle(!socks5Enabled) })
	}
	autoReconnectItem := settings.AddSubMenuItemCheckbox("Auto Reconnect", "", autoReconnectEnabled)
	if currentProfile == nil {
		autoReconnectItem.Disable()
	} else {
		onClick(ctx, autoReconnectItem, func(context.Context) {
			m.handleAutoReconnectToggle(!autoReconnectEnabled)
		})
	}
	applyDNSItem := settings.AddSubMenuItemCheckbox("Apply DNS", "", applyDNSEnabled)
	if currentProfile == nil {
		applyDNSItem.Disable()
	} else {
		onClick(ctx, applyDNSItem, func(context.Context) {
			m.handleApplyDNSToggle(!applyDNSEnabled)
		})
	}
	copyItem := settings.AddSubMenuItem("Copy Diagnostics", "")
	onClick(ctx, copyItem, func(context.Context) { m.copyDiagnostics() })
	systraylib.AddSeparator()
	quit := systraylib.AddMenuItem("Quit", "Quit the app")
	onClick(ctx, quit, func(context.Context) { systraylib.Quit() })
}

func statusCurrentID(status *types.Status) string {
	if status == nil {
		return ""
	}
	return status.CurrentProfileID
}

func currentProfileByID(profiles []types.Profile, id string) *types.Profile {
	for _, profile := range profiles {
		if profile.ID == id {
			current := profile
			return &current
		}
	}
	return nil
}

func (m *Menu) handleSocks5Toggle(enabled bool) {
	m.mu.Lock()
	status := copyStatus(m.status)
	m.mu.Unlock()
	req := types.ProfileUpdateRequest{SOCKS5Enabled: &enabled}
	if status == nil || status.CurrentProfileID == "" {
		systrayLog.Error(fmt.Errorf("no profile selected"))
		return
	}
	if _, err := m.Client.UpdateProfile(context.Background(), status.CurrentProfileID, req); err != nil {
		systrayLog.Error(err)
		return
	}
	m.refresh(context.Background())
	m.requestRebuild()
}

func (m *Menu) handleAutoReconnectToggle(enabled bool) {
	m.mu.Lock()
	status := copyStatus(m.status)
	m.mu.Unlock()
	if status == nil || status.CurrentProfileID == "" {
		systrayLog.Error(fmt.Errorf("no profile selected"))
		return
	}
	req := types.ProfileUpdateRequest{AutoReconnect: &enabled}
	if _, err := m.Client.UpdateProfile(context.Background(), status.CurrentProfileID, req); err != nil {
		systrayLog.Error(err)
		return
	}
	m.refresh(context.Background())
	m.requestRebuild()
}

func (m *Menu) handleApplyDNSToggle(enabled bool) {
	m.mu.Lock()
	status := copyStatus(m.status)
	m.mu.Unlock()
	if status == nil || status.CurrentProfileID == "" {
		systrayLog.Error(fmt.Errorf("no profile selected"))
		return
	}
	req := types.ProfileUpdateRequest{ApplyDNS: &enabled}
	if _, err := m.Client.UpdateProfile(context.Background(), status.CurrentProfileID, req); err != nil {
		systrayLog.Error(err)
		return
	}
	m.refresh(context.Background())
	m.requestRebuild()
}

func (m *Menu) handleToggle(action toggleAction) {
	ctx := context.Background()
	switch action {
	case toggleConnect:
		if err := m.Client.ConnectCurrent(ctx); err != nil {
			systrayLog.Error(err)
		}
	case toggleDisconnect:
		if err := m.Client.Disconnect(ctx); err != nil {
			systrayLog.Error(err)
		}
	}
	m.refresh(ctx)
	m.requestRebuild()
}

func (m *Menu) copyDiagnostics() {
	text, err := m.Client.DiagnosticsText(context.Background())
	if err != nil {
		systrayLog.Error(err)
		return
	}
	if err := writeClipboard(text); err != nil {
		systrayLog.Error(err)
	}
}

func (m *Menu) watch() {
	m.init()
	go func() {
		for range m.rebuildCh {
			m.rebuild()
		}
	}()
	for {
		watcher, err := m.Client.Watch(context.Background())
		if err != nil {
			systrayLog.Error(err)
			return
		}
		for {
			notify, err := watcher.Next()
			if err != nil {
				_ = watcher.Close()
				systrayLog.Error(err)
				return
			}
			changed := false
			m.mu.Lock()
			if notify.Status != nil {
				m.status = notify.Status
				changed = true
			}
			if notify.Profiles != nil {
				m.profiles = append([]types.Profile(nil), notify.Profiles...)
				changed = true
			}
			m.mu.Unlock()
			if changed {
				m.refresh(context.Background())
				m.requestRebuild()
			}
		}
	}
}

func writeClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("powershell", "-NoProfile", "-Command", "Set-Clipboard")
	case "darwin":
		cmd = exec.Command("pbcopy")
	default:
		cmd = exec.Command("sh", "-c", "command -v wl-copy >/dev/null 2>&1 && wl-copy || xclip -selection clipboard")
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func onClick(ctx context.Context, item *systraylib.MenuItem, fn func(context.Context)) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-item.ClickedCh:
				fn(ctx)
			}
		}
	}()
}

func addDisabled(title string) {
	item := systraylib.AddMenuItem(title, "")
	item.Disable()
}

func setTooltip(text string) {
	if runtime.GOOS == "linux" {
		systraylib.SetTitle(text)
		return
	}
	systraylib.SetTooltip(text)
}

func setTrayIcon() {
	// Use PNG on Linux for better tray backend compatibility.
	// Keep ICO for non-Linux platforms.
	if runtime.GOOS == "linux" {
		systraylib.SetIcon(brandicons.Favicon32PNG())
		time.Sleep(10 * time.Millisecond)
		return
	}
	systraylib.SetIcon(brandicons.TrayICO())
}

func toggleForStatus(status *types.Status) toggleState {
	if status == nil {
		return toggleState{Title: "Connect", Enabled: true, Action: toggleConnect}
	}
	switch status.State {
	case types.StateConnected:
		return toggleState{Title: "Disconnect", Enabled: true, Action: toggleDisconnect}
	case types.StateConnecting, types.StateReconnecting:
		return toggleState{Title: string(status.State), Enabled: false, Action: toggleNone}
	default:
		return toggleState{Title: "Connect", Enabled: true, Action: toggleConnect}
	}
}

func tooltipText(status *types.Status, profiles []types.Profile) string {
	if status == nil {
		return "FlexConnect: unavailable"
	}
	parts := []string{"FlexConnect: " + string(status.State)}
	if status.CurrentProfileID != "" {
		parts = append(parts, "profile: "+profileNameByID(profiles, status.CurrentProfileID))
	}
	if status.Session != nil && status.Session.VPNAddress != "" {
		parts = append(parts, "VPN IP: "+status.Session.VPNAddress)
	}
	if status.LastError != "" {
		parts = append(parts, "error: "+status.LastError)
	}
	return strings.Join(parts, " | ")
}

func diagnosticsSummaryRows(diag *types.Diagnostics, status *types.Status, profiles []types.Profile) []string {
	if status == nil && diag != nil {
		statusCopy := diag.Status
		status = &statusCopy
	}
	rows := []string{}
	if diag != nil {
		if strings.TrimSpace(diag.Version) != "" {
			rows = append(rows, "Version: "+diag.Version)
		}
		if diag.Traffic != nil {
			rows = append(rows, "Traffic Sent: "+formatByteSize(diag.Traffic.BytesSent))
			rows = append(rows, "Traffic Received: "+formatByteSize(diag.Traffic.BytesReceived))
		}
		if strings.TrimSpace(diag.GeneratedAt) != "" {
			rows = append(rows, "Generated: "+diag.GeneratedAt)
		}
	}
	rows = append(rows, "State: "+stateText(status))
	if status != nil {
		if status.CurrentProfileID != "" {
			rows = append(rows, "Profile: "+profileNameByID(profiles, status.CurrentProfileID))
		}
		if status.Session != nil {
			if strings.TrimSpace(status.Session.ServerAddress) != "" {
				rows = append(rows, "Server: "+status.Session.ServerAddress)
			}
			if strings.TrimSpace(status.Session.VPNAddress) != "" {
				rows = append(rows, "VPN IP: "+status.Session.VPNAddress)
			}
		}
		rows = append(rows, fmt.Sprintf("Routes: %d effective", len(status.EffectiveRoutes)))
		if status.SOCKS5Enabled {
			if strings.TrimSpace(status.SOCKS5Listen) != "" {
				rows = append(rows, "SOCKS5: "+status.SOCKS5Listen)
			} else {
				rows = append(rows, "SOCKS5: enabled")
			}
		} else {
			rows = append(rows, "SOCKS5: disabled")
		}
		if status.LastError != "" {
			rows = append(rows, "Last Error: "+status.LastError)
		}
	}
	if len(rows) == 0 {
		rows = append(rows, "Diagnostics unavailable")
	}
	return rows
}

func diagnosticsDetailRows(diag *types.Diagnostics, status *types.Status, profiles []types.Profile) []string {
	if status == nil && diag != nil {
		statusCopy := diag.Status
		status = &statusCopy
	}
	rows := diagnosticsSummaryRows(diag, status, profiles)
	if status != nil && status.Session != nil {
		s := status.Session
		rows = append(rows, "-", "Session")
		if strings.TrimSpace(s.Hostname) != "" {
			rows = append(rows, "Hostname: "+s.Hostname)
		}
		if strings.TrimSpace(s.TUNName) != "" {
			rows = append(rows, "Tunnel: "+s.TUNName)
		}
		if strings.TrimSpace(s.VPNMask) != "" {
			rows = append(rows, "VPN Mask: "+s.VPNMask)
		}
		rows = append(rows, fmt.Sprintf("MTU: %d", s.MTU))
		if len(s.DNS) > 0 {
			rows = append(rows, "DNS: "+strings.Join(s.DNS, ", "))
		}
		if strings.TrimSpace(s.TLSCipher) != "" {
			rows = append(rows, "TLS: "+s.TLSCipher)
		}
		if strings.TrimSpace(s.DTLSCipher) != "" {
			rows = append(rows, "DTLS: "+s.DTLSCipher)
		}
	}
	if diag != nil && diag.Traffic != nil {
		rows = append(rows, "-", "Traffic")
		rows = append(rows, "Sent: "+formatByteSize(diag.Traffic.BytesSent))
		rows = append(rows, "Received: "+formatByteSize(diag.Traffic.BytesReceived))
	}
	if diag != nil && diag.CurrentProfile != nil {
		p := *diag.CurrentProfile
		rows = append(rows, "-", "Current Profile")
		rows = append(rows, "Name: "+profileTitle(p))
		if strings.TrimSpace(p.ServerURL) != "" {
			rows = append(rows, "Server: "+p.ServerURL)
		}
		if strings.TrimSpace(p.Username) != "" {
			rows = append(rows, "Username: "+p.Username)
		}
		if strings.TrimSpace(p.Group) != "" {
			rows = append(rows, "Group: "+p.Group)
		}
		rows = append(rows, fmt.Sprintf("Accept Routes: %t", p.AcceptServerRoutes))
		rows = append(rows, fmt.Sprintf("Auto Reconnect: %t", types.BoolValue(p.AutoReconnect, false)))
		rows = append(rows, fmt.Sprintf("Apply DNS: %t", types.BoolValue(p.ApplyDNS, true)))
		if len(p.CustomInclude) > 0 {
			rows = append(rows, "Include: "+strings.Join(p.CustomInclude, ", "))
		}
		if len(p.CustomExclude) > 0 {
			rows = append(rows, "Exclude: "+strings.Join(p.CustomExclude, ", "))
		}
	}
	if status != nil && len(status.EffectiveRoutes) > 0 {
		rows = append(rows, "-", "Routes")
		limit := min(len(status.EffectiveRoutes), 8)
		for _, route := range status.EffectiveRoutes[:limit] {
			rows = append(rows, fmt.Sprintf("%s %s metric=%d", route.Action, route.Destination, route.Metric))
		}
		if len(status.EffectiveRoutes) > limit {
			rows = append(rows, fmt.Sprintf("... %d more", len(status.EffectiveRoutes)-limit))
		}
	}
	if diag != nil && len(diag.ServerConfig) > 0 {
		keys := make([]string, 0, len(diag.ServerConfig))
		for key := range diag.ServerConfig {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		rows = append(rows, "-", "Server Config")
		for _, key := range keys {
			rows = append(rows, fmt.Sprintf("%s: %v", key, diag.ServerConfig[key]))
		}
	}
	if diag != nil && len(diag.Logs) > 0 {
		rows = append(rows, "-", "Recent Logs")
		start := max(0, len(diag.Logs)-5)
		for _, entry := range diag.Logs[start:] {
			rows = append(rows, fmt.Sprintf("%s %s: %s", entry.Time, entry.Level, entry.Message))
		}
	}
	return rows
}

func profileTitle(profile types.Profile) string {
	if strings.TrimSpace(profile.Name) != "" {
		return profile.Name
	}
	if strings.TrimSpace(profile.ServerURL) != "" {
		return profile.ServerURL
	}
	if strings.TrimSpace(profile.ID) != "" {
		return profile.ID
	}
	return "Profile"
}

func profileNameByID(profiles []types.Profile, id string) string {
	for _, profile := range profiles {
		if profile.ID == id {
			return profileTitle(profile)
		}
	}
	return id
}

func stateText(status *types.Status) string {
	if status == nil || status.State == "" {
		return "Unavailable"
	}
	return string(status.State)
}

func copyStatus(status *types.Status) *types.Status {
	if status == nil {
		return nil
	}
	copy := *status
	if status.Session != nil {
		session := *status.Session
		copy.Session = &session
	}
	copy.EffectiveRoutes = append([]types.RouteSpec(nil), status.EffectiveRoutes...)
	return &copy
}

func copyDiagnostics(diag *types.Diagnostics) *types.Diagnostics {
	if diag == nil {
		return nil
	}
	copy := *diag
	if diag.CurrentProfile != nil {
		profile := *diag.CurrentProfile
		copy.CurrentProfile = &profile
	}
	copy.Profiles = append([]types.Profile(nil), diag.Profiles...)
	copy.Logs = append([]types.LogEntry(nil), diag.Logs...)
	if diag.ServerConfig != nil {
		copy.ServerConfig = map[string]any{}
		for key, value := range diag.ServerConfig {
			copy.ServerConfig[key] = value
		}
	}
	if diag.Traffic != nil {
		traffic := *diag.Traffic
		copy.Traffic = &traffic
	}
	return &copy
}

func formatByteSize(bytes uint64) string {
	const (
		kibi = 1024
		mebi = 1024 * 1024
		gibi = 1024 * 1024 * 1024
	)
	switch {
	case bytes >= gibi:
		return fmt.Sprintf("%.2f GiB", float64(bytes)/float64(gibi))
	case bytes >= mebi:
		return fmt.Sprintf("%.2f MiB", float64(bytes)/float64(mebi))
	case bytes >= kibi:
		return fmt.Sprintf("%.2f KiB", float64(bytes)/float64(kibi))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
