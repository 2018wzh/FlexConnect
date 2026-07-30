package systray

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	systraylib "fyne.io/systray"
	"github.com/gen2brain/beeep"

	"flexconnect/client/local"
	"flexconnect/internal/logging"
	"flexconnect/internal/types"
)

var systrayLog = logging.WithComponent("flexconnect-systray")

type notificationSender interface {
	Send(title, body string) error
	Alert(title, body string) error
}

type beeepNotificationSender struct{}

func (beeepNotificationSender) Send(title, body string) error {
	return beeep.Notify(title, body, "")
}

func (beeepNotificationSender) Alert(title, body string) error {
	return beeep.Alert(title, body, "")
}

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
	notifier   notificationSender
	notified   map[string]struct{}
	errorSeen  map[string]time.Time
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
	go m.openLoop(ctx)
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
	if m.notifier == nil {
		m.notifier = beeepNotificationSender{}
	}
	if m.notified == nil {
		m.notified = map[string]struct{}{}
	}
	if m.errorSeen == nil {
		m.errorSeen = map[string]time.Time{}
	}
}

// reportError records every tray error and presents a bounded, deduplicated
// alert. Reconnect/watch loops can encounter the same IPC error repeatedly;
// suppressing only identical errors for a short interval keeps the desktop
// usable while retaining the full error in the component log.
func (m *Menu) reportError(operation string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	m.init()
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 320 {
		message = message[:320] + "..."
	}
	key := operation + ":" + message
	now := time.Now()
	m.mu.Lock()
	last, seen := m.errorSeen[key]
	if seen && now.Sub(last) < 30*time.Second {
		m.mu.Unlock()
		return
	}
	m.errorSeen[key] = now
	if len(m.errorSeen) > 64 {
		for k, at := range m.errorSeen {
			if now.Sub(at) >= 30*time.Second {
				delete(m.errorSeen, k)
			}
		}
	}
	notifier := m.notifier
	m.mu.Unlock()

	wrapped := fmt.Errorf("%s: %w", operation, err)
	systrayLog.Error(wrapped)
	if notifier != nil {
		if notifyErr := notifier.Alert("FlexConnect tray error", operation+"\n\n"+message); notifyErr != nil {
			systrayLog.Error(fmt.Errorf("desktop error alert failed operation=%s: %w", operation, notifyErr))
		}
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
		m.reportError("daemon unavailable", err)
	}
	profiles, err := m.Client.Profiles(ctx)
	if err != nil {
		m.reportError("daemon unavailable", err)
	}
	traffic, err := m.Client.Traffic(ctx)
	if err != nil {
		m.reportError("daemon unavailable", err)
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
			m.reportError("disconnect failed", err)
			return
		}
	}

	if err := m.Client.SwitchProfile(ctx, profileID); err != nil {
		m.reportError("switch profile failed", err)
		return
	}
	if status != nil && status.State == types.StateConnected {
		if err := m.Client.Connect(ctx, profileID); err != nil {
			m.reportError("reconnect after profile switch failed", err)
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
		m.reportError("update SOCKS5 setting failed", fmt.Errorf("no profile selected"))
		return
	}
	if _, err := m.Client.UpdateProfile(context.Background(), status.CurrentProfileID, req); err != nil {
		m.reportError("update SOCKS5 setting failed", err)
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
		m.reportError("update auto-reconnect setting failed", fmt.Errorf("no profile selected"))
		return
	}
	req := types.ProfileUpdateRequest{AutoReconnect: &enabled}
	if _, err := m.Client.UpdateProfile(context.Background(), status.CurrentProfileID, req); err != nil {
		m.reportError("update auto-reconnect setting failed", err)
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
		m.reportError("update DNS setting failed", fmt.Errorf("no profile selected"))
		return
	}
	req := types.ProfileUpdateRequest{ApplyDNS: &enabled}
	if _, err := m.Client.UpdateProfile(context.Background(), status.CurrentProfileID, req); err != nil {
		m.reportError("update DNS setting failed", err)
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
			m.reportError("connect failed", err)
		}
	case toggleDisconnect:
		if err := m.Client.Disconnect(ctx); err != nil {
			m.reportError("disconnect failed", err)
		}
	}
	m.refresh(ctx)
	m.requestRebuild()
}

func (m *Menu) copyDiagnostics() {
	text, err := m.Client.DiagnosticsText(context.Background())
	if err != nil {
		m.reportError("read diagnostics failed", err)
		return
	}
	if err := writeClipboard(text); err != nil {
		m.reportError("copy diagnostics failed", err)
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

// updateTrayTooltip sets the tray hover tooltip from current state without
// resetting the menu. Safe to call on every traffic tick.
func (m *Menu) updateTrayTooltip() {
	m.mu.Lock()
	status := copyStatus(m.status)
	traffic := m.traffic
	profiles := append([]types.Profile(nil), m.profiles...)
	m.mu.Unlock()
	setTooltip(tooltipText(status, traffic, profiles))
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

func (m *Menu) openLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-systraylib.TrayOpenedCh:
			m.refresh(ctx)
			m.requestRebuild()
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
			m.reportError("watch connection failed", err)
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
				m.reportError("watch stream failed", err)
				break
			}
			m.handleNotify(notify, m.updateTrayTooltip)
		}
		if !waitForRetry(ctx) {
			return
		}
	}
}

func (m *Menu) handleNotify(notify types.Notify, updateTooltip func()) {
	stateChanged := false
	trafficChanged := false
	m.mu.Lock()
	if notify.Status != nil {
		m.status = notify.Status
		stateChanged = true
	}
	if notify.Profiles != nil {
		m.profiles = append([]types.Profile(nil), notify.Profiles...)
		stateChanged = true
	}
	if notify.Traffic != nil {
		m.traffic = *notify.Traffic
		trafficChanged = true
	}
	m.mu.Unlock()
	if notify.Connection != nil {
		m.notifyConnection(*notify.Connection)
	}
	if trafficChanged && !stateChanged {
		updateTooltip()
	}
	if stateChanged {
		m.requestRebuild()
	}
}

func (m *Menu) notifyConnection(event types.ConnectionEvent) {
	if event.Kind != "connection_lost" && event.Kind != "reconnect_scheduled" && event.Kind != "reconnected" && event.Kind != "reconnect_failed" {
		return
	}
	m.init()
	key := event.ID
	if event.ConnectionID != "" {
		key = event.ConnectionID + ":" + event.Kind
	}
	m.mu.Lock()
	if _, ok := m.notified[key]; ok {
		m.mu.Unlock()
		return
	}
	m.notified[key] = struct{}{}
	notifier := m.notifier
	m.mu.Unlock()

	title := "FlexConnect"
	body := event.Kind
	switch event.Kind {
	case "connection_lost":
		title = "VPN connection lost"
		body = event.ReasonCode
		if event.Error != "" {
			body += ": " + event.Error
		}
	case "reconnect_scheduled":
		title = "VPN reconnecting"
		body = fmt.Sprintf("Attempt %d scheduled", event.Attempt)
	case "reconnected":
		title = "VPN reconnected"
		body = "The VPN session was restored."
	case "reconnect_failed":
		title = "VPN reconnect failed"
		body = fmt.Sprintf("Attempt %d", event.Attempt)
		if event.Error != "" {
			body += ": " + event.Error
		}
	}
	if err := notifier.Send(title, body); err != nil {
		systrayLog.Error(fmt.Errorf("desktop notification failed event=%s: %w", event.Kind, err))
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

// maxTooltipLen is the Windows NOTIFYICONDATA szTip limit (128 WCHARs,
// minus one for the null terminator). We count runes, not bytes, because
// each BMP character (including ↑↓…) occupies one WCHAR.
const maxTooltipLen = 127

func tooltipText(status *types.Status, traffic types.TrafficSnapshot, profiles []types.Profile) string {
	state := stateText(status)

	// Line 1: state – always shown in full (short).
	line1 := fmt.Sprintf("%s", state)

	// Line 3: traffic & speed – always shown (use "0" when idle, never hide).
	speed := fmt.Sprintf("\u2191%s/s \u2193%s/s",
		formatByteSizeCompact(uint64(traffic.BytesSentPerSecond)),
		formatByteSizeCompact(uint64(traffic.BytesReceivedPerSecond)))
	line3 := speed

	// Line 2: identity – may be truncated when total exceeds maxTooltipLen.
	identity := trafficIdentity(status, profiles)

	// Calculate how many characters remain for the identity line using
	// rune count (matching Windows WCHAR semantics).
	budget := maxTooltipLen - len([]rune(line1)) - 1 - len([]rune(line3)) - 1
	if budget < 0 {
		budget = 0
	}
	line2 := truncateTo(identity, budget)

	return line1 + "\n" + line2 + "\n" + line3
}

// truncateTo returns s if it fits within max, otherwise shortens it with "…".
func truncateTo(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

// formatByteSizeCompact produces short labels with B/KB/MB/GB suffixes
// (e.g. "512B", "1.5KB", "2.3MB").
func formatByteSizeCompact(bytes uint64) string {
	const (
		kibi = 1024
		mebi = 1024 * 1024
		gibi = 1024 * 1024 * 1024
	)
	switch {
	case bytes >= gibi:
		return fmt.Sprintf("%.1fGB", float64(bytes)/float64(gibi))
	case bytes >= mebi:
		return fmt.Sprintf("%.1fMB", float64(bytes)/float64(mebi))
	case bytes >= kibi:
		return fmt.Sprintf("%.1fKB", float64(bytes)/float64(kibi))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

func trafficSummaryRows(status *types.Status, traffic types.TrafficSnapshot, profiles []types.Profile) []string {
	rows := []string{"Status: " + stateText(status)}

	// Profile name.
	profileName := profileNameForStatus(status, profiles)
	if profileName != "" {
		rows = append(rows, "Profile: "+profileName)
	}

	// VPN IP from session.
	if status != nil && status.Session != nil {
		if ip := strings.TrimSpace(status.Session.VPNAddress); ip != "" {
			rows = append(rows, "IP: "+ip)
		}
	}

	rows = append(rows,
		fmt.Sprintf("Traffic: ↑%s ↓%s", formatByteSizeCompact(traffic.BytesSent), formatByteSizeCompact(traffic.BytesReceived)),
	)
	return rows
}

// profileNameForStatus returns the display name of the current profile, or empty.
func profileNameForStatus(status *types.Status, profiles []types.Profile) string {
	if status == nil || status.CurrentProfileID == "" {
		return ""
	}
	return profileNameByID(profiles, status.CurrentProfileID)
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
