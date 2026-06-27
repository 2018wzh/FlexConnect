package systray

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	systraylib "fyne.io/systray"

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

	rebuildMu  sync.Mutex
	mu         sync.Mutex
	status     *types.Status
	traffic    types.TrafficSnapshot
	profiles   []types.Profile
	rebuildCh  chan struct{}
	runCancel  context.CancelFunc
	menuCancel context.CancelFunc
}

func (m *Menu) Run() {
	if m.Client == nil {
		m.Client = &local.Client{}
	}
	systraylib.SetTitle("FlexConnect")
	systraylib.Run(m.onReady, m.onExit)
}

func (m *Menu) onReady() {
	setTrayIconColor(trayIconBlue)
	setTooltip("FlexConnect")
	m.init()
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.runCancel = cancel
	m.mu.Unlock()
	m.refresh(ctx)
	m.rebuild()
	go m.rebuildLoop(ctx)
	go m.watch(ctx)
}

func (m *Menu) onExit() {
	m.mu.Lock()
	if m.runCancel != nil {
		m.runCancel()
	}
	if m.menuCancel != nil {
		m.menuCancel()
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
	traffic, err := m.Client.Traffic(ctx)
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
	if traffic != nil {
		m.traffic = *traffic
	}
}

func (m *Menu) rebuild() {
	m.rebuildMu.Lock()
	defer m.rebuildMu.Unlock()

	m.mu.Lock()
	status := copyStatus(m.status)
	traffic := m.traffic
	profiles := append([]types.Profile(nil), m.profiles...)
	if m.menuCancel != nil {
		m.menuCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.menuCancel = cancel
	m.mu.Unlock()

	m.renderMenu(ctx, buildMenuModel(status, traffic, profiles))
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

func (m *Menu) renderMenu(ctx context.Context, model menuModel) {
	systraylib.ResetMenu()
	setTrayIconColor(model.Icon)
	setTooltip(model.Tooltip)
	for _, item := range model.Items {
		m.renderMenuItem(ctx, nil, item)
	}
}

func (m *Menu) renderMenuItem(ctx context.Context, parent *systraylib.MenuItem, item menuItemModel) {
	if item.Separator {
		if parent == nil {
			systraylib.AddSeparator()
		} else {
			parent.AddSeparator()
		}
		return
	}

	menuItem := addMenuItem(parent, item)
	if item.Checked {
		menuItem.Check()
	}
	if item.Disabled {
		menuItem.Disable()
	} else {
		m.bindMenuAction(ctx, menuItem, item)
	}
	for _, child := range item.Children {
		m.renderMenuItem(ctx, menuItem, child)
	}
}

func addMenuItem(parent *systraylib.MenuItem, item menuItemModel) *systraylib.MenuItem {
	if parent == nil {
		if item.Checkbox {
			return systraylib.AddMenuItemCheckbox(item.Title, item.Tooltip, item.Checked)
		}
		return systraylib.AddMenuItem(item.Title, item.Tooltip)
	}
	if item.Checkbox {
		return parent.AddSubMenuItemCheckbox(item.Title, item.Tooltip, item.Checked)
	}
	return parent.AddSubMenuItem(item.Title, item.Tooltip)
}

func (m *Menu) bindMenuAction(ctx context.Context, item *systraylib.MenuItem, model menuItemModel) {
	switch model.Action {
	case menuActionToggle:
		onClick(ctx, item, func(context.Context) { m.handleToggle(model.Toggle) })
	case menuActionProfile:
		onClick(ctx, item, func(ctx context.Context) { m.handleProfileSelection(ctx, model.ProfileID) })
	case menuActionSocks5:
		onClick(ctx, item, func(context.Context) { m.handleSocks5Toggle(model.Value) })
	case menuActionAutoReconnect:
		onClick(ctx, item, func(context.Context) { m.handleAutoReconnectToggle(model.Value) })
	case menuActionApplyDNS:
		onClick(ctx, item, func(context.Context) { m.handleApplyDNSToggle(model.Value) })
	case menuActionCopyDiagnostics:
		onClick(ctx, item, func(context.Context) { m.copyDiagnostics() })
	case menuActionQuit:
		onClick(ctx, item, func(context.Context) { systraylib.Quit() })
	}
}

func (m *Menu) rebuildLoop(ctx context.Context) {
	m.init()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.rebuildCh:
			m.drainRebuilds()
			m.rebuild()
		}
	}
}

func (m *Menu) drainRebuilds() {
	for {
		select {
		case <-m.rebuildCh:
		default:
			return
		}
	}
}

func (m *Menu) watch(ctx context.Context) {
	m.init()
	for {
		watcher, err := m.Client.Watch(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			systrayLog.Error(err)
			if !waitForRetry(ctx) {
				return
			}
			continue
		}
		for {
			notify, err := watcher.Next()
			if err != nil {
				_ = watcher.Close()
				if ctx.Err() != nil {
					return
				}
				systrayLog.Error(err)
				break
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
			if notify.Traffic != nil {
				m.traffic = *notify.Traffic
				changed = true
			}
			m.mu.Unlock()
			if changed {
				m.requestRebuild()
			}
		}
		if !waitForRetry(ctx) {
			return
		}
	}
}

func waitForRetry(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
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
			case _, ok := <-item.ClickedCh:
				if !ok || ctx.Err() != nil {
					return
				}
				fn(ctx)
			}
		}
	}()
}

func setTooltip(text string) {
	if runtime.GOOS == "linux" {
		systraylib.SetTitle(text)
		return
	}
	systraylib.SetTooltip(text)
}

func setTrayIconColor(color trayIconColor) {
	systraylib.SetIcon(trayIconForColor(color))
	if runtime.GOOS == "linux" {
		time.Sleep(10 * time.Millisecond)
	}
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

func tooltipText(status *types.Status, traffic types.TrafficSnapshot, profiles []types.Profile) string {
	return strings.Join(trafficSummaryRows(status, traffic, profiles), "\n")
}

func trafficSummaryRows(status *types.Status, traffic types.TrafficSnapshot, profiles []types.Profile) []string {
	rows := []string{"FlexConnect: " + stateText(status)}
	identity := trafficIdentity(status, profiles)
	if identity != "" {
		rows = append(rows, identity)
	}
	rows = append(rows,
		fmt.Sprintf("Traffic ↑%s ↓%s", formatByteSize(traffic.BytesSent), formatByteSize(traffic.BytesReceived)),
		fmt.Sprintf("Speed ↑%s ↓%s", formatByteRate(traffic.BytesSentPerSecond), formatByteRate(traffic.BytesReceivedPerSecond)),
	)
	return rows
}

func trafficIdentity(status *types.Status, profiles []types.Profile) string {
	if status == nil {
		return ""
	}
	parts := []string{}
	if status.CurrentProfileID != "" {
		parts = append(parts, profileNameByID(profiles, status.CurrentProfileID))
	}
	if status.Session != nil && strings.TrimSpace(status.Session.VPNAddress) != "" {
		parts = append(parts, status.Session.VPNAddress)
	}
	return strings.Join(parts, " · ")
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

func formatByteRate(bytesPerSecond float64) string {
	return formatByteFloat(bytesPerSecond) + "/s"
}

func formatByteFloat(bytes float64) string {
	const (
		kibi = 1024
		mebi = 1024 * 1024
		gibi = 1024 * 1024 * 1024
	)
	switch {
	case bytes >= gibi:
		return fmt.Sprintf("%.2f GiB", bytes/float64(gibi))
	case bytes >= mebi:
		return fmt.Sprintf("%.2f MiB", bytes/float64(mebi))
	case bytes >= kibi:
		return fmt.Sprintf("%.2f KiB", bytes/float64(kibi))
	default:
		return fmt.Sprintf("%.0f B", bytes)
	}
}
