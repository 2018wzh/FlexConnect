package appd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"flexconnect/internal/logbuf"
	"flexconnect/internal/logging"
	"flexconnect/internal/profileio"
	"flexconnect/internal/router"
	"flexconnect/internal/secret"
	"flexconnect/internal/socks5"
	storefile "flexconnect/internal/store/file"
	"flexconnect/internal/types"
	"flexconnect/internal/vpn"
)

const version = "1.0.4"

var (
	autoReconnectMinDelay = 2 * time.Second
	autoReconnectMaxDelay = 1 * time.Minute
)

var appdDebug bool
var errNoCurrentProfile = errors.New("no current profile selected")
var errConnectInProgress = errors.New("connection already in progress")
var appdLog = logging.WithComponent("appd")
var appdDebugLog = logging.WithComponent("appd")

func SetDebug(enabled bool) {
	appdDebug = enabled
}

func appdDebugf(format string, args ...any) {
	if !appdDebug {
		return
	}
	appdDebugLog.Debugf(format, args...)
}

type Store interface {
	Load() (storefile.Data, error)
	Save(storefile.Data) error
}

type Daemon interface {
	Status() types.Status
	ListProfiles() []types.Profile
	CurrentProfile() (types.Profile, error)
	CreateProfile(types.Profile, string) (types.Profile, error)
	UpdateProfile(string, types.ProfileUpdateRequest) (types.Profile, error)
	DeleteProfile(string) error
	SwitchProfile(context.Context, string) error
	ConnectCurrent(context.Context) error
	Connect(context.Context, string) error
	Disconnect(context.Context) error
	UpdateRoutes(string, types.RouteUpdateRequest) (types.Profile, error)
	Login(context.Context, types.LoginRequest) error
	Diagnostics() types.Diagnostics
	Logs() []types.LogEntry
	Watch(context.Context) <-chan types.Notify
}

type Service struct {
	mu                  sync.Mutex
	store               Store
	secrets             secret.Store
	backend             vpn.Backend
	planner             router.Planner
	profiles            []types.Profile
	currentID           string
	connectedID         string
	status              types.Status
	logs                *logbuf.Buffer
	watchers            map[int]chan types.Notify
	nextWatcherID       int
	proxyServer         *socks5.Server
	disconnectSeq       uint64
	manualDisconnectSeq uint64
	manualProfileID     string
	reconnectTimer      *time.Timer
	reconnectProfileID  string
	reconnectAttempt    int
	reconnectSeq        uint64
	reconnectID         uint64
}

func New(store Store, secrets secret.Store, backend vpn.Backend, planner router.Planner) (*Service, error) {
	appdLog.Printf("creating service")
	s := &Service{
		store:    store,
		secrets:  secrets,
		backend:  backend,
		planner:  planner,
		logs:     logbuf.New(500),
		status:   types.Status{State: types.StateDisconnected, UpdatedAt: now()},
		watchers: map[int]chan types.Notify{},
	}
	if err := s.load(); err != nil {
		appdLog.Printf("load state failed err=%v", err)
		return nil, err
	}
	appdLog.Printf("loaded service count=%d", len(s.profiles))
	appdDebugf("service initialized current=%s profile_count=%d", s.currentID, len(s.profiles))
	go s.consumeBackendEvents()
	return s, nil
}

func (s *Service) load() error {
	data, err := s.store.Load()
	if err != nil {
		appdLog.Printf("store load failed err=%v", err)
		return err
	}
	appdLog.Printf("loaded state current_id=%s total_profiles=%d", data.CurrentProfileID, len(data.Profiles))
	s.profiles = data.Profiles
	s.currentID = data.CurrentProfileID
	for i := range s.profiles {
		if s.profiles[i].SecretRef == "" {
			s.profiles[i].SecretRef = "profile/" + s.profiles[i].ID
			s.profiles[i].UpdatedAt = now()
		}
		s.profiles[i] = profileio.NormalizeProfile(s.profiles[i])
	}
	if s.currentID != "" {
		if _, err := s.findProfileLocked(s.currentID); err != nil {
			s.currentID = ""
		}
	}
	if s.currentID == "" && len(s.profiles) == 1 {
		s.currentID = s.profiles[0].ID
	}
	s.status.CurrentProfileID = s.currentID
	appdDebugf("state loaded current=%s profile_count=%d", s.currentID, len(s.profiles))
	return s.persist()
}

func (s *Service) persist() error {
	err := s.store.Save(storefile.Data{
		Profiles:         s.profiles,
		CurrentProfileID: s.currentID,
	})
	if err != nil {
		appdLog.Printf("failed to persist state err=%v", err)
		return err
	}
	appdLog.Printf("persisted state current_id=%s profile_count=%d", s.currentID, len(s.profiles))
	return nil
}

func (s *Service) Status() types.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Service) Logs() []types.LogEntry {
	return s.logs.Snapshot()
}

func (s *Service) Diagnostics() types.Diagnostics {
	s.mu.Lock()
	defer s.mu.Unlock()

	status := s.status
	profiles := append([]types.Profile(nil), s.profiles...)
	var traffic *types.TrafficStats
	if status.State == types.StateConnected {
		if session := s.backend.SessionInfo(); session != nil {
			status.Session = session
		}
		if t := s.backend.Traffic(); t != nil {
			traffic = t
		}
	}
	var current *types.Profile
	for _, profile := range profiles {
		if profile.ID == s.currentID {
			cp := profile
			current = &cp
			break
		}
	}
	return types.Diagnostics{
		Version:        version,
		Status:         status,
		CurrentProfile: current,
		Profiles:       profiles,
		ServerConfig:   s.backend.ReadServerConfig(),
		Traffic:        traffic,
		Logs:           s.logs.Snapshot(),
		GeneratedAt:    now(),
	}
}

func (s *Service) ListProfiles() []types.Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]types.Profile(nil), s.profiles...)
	slices.SortFunc(out, func(a, b types.Profile) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out
}

func (s *Service) CurrentProfile() (types.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentID == "" {
		return types.Profile{}, errNoCurrentProfile
	}
	return s.findProfileLocked(s.currentID)
}

func (s *Service) CreateProfile(profile types.Profile, password string) (types.Profile, error) {
	appdLog.Printf("create profile name=%q server=%q", profile.Name, profile.ServerURL)
	s.mu.Lock()
	defer s.mu.Unlock()
	if profile.ID == "" {
		profile = types.NewProfile(profile.Name)
	}
	profile = profileio.NormalizeProfile(profile)
	profile.UpdatedAt = now()
	if profile.CreatedAt == "" {
		profile.CreatedAt = profile.UpdatedAt
	}
	if profile.SecretRef == "" {
		profile.SecretRef = "profile/" + profile.ID
	}
	s.profiles = append(s.profiles, profile)
	if s.currentID == "" {
		s.currentID = profile.ID
		s.status.CurrentProfileID = profile.ID
	}
	if password != "" {
		_ = s.secrets.Put(profile.SecretRef, password)
	}
	if err := s.persist(); err != nil {
		return types.Profile{}, err
	}
	s.logs.Add("info", fmt.Sprintf("appd: profile created id=%s name=%q", profile.ID, profile.Name))
	s.emitLocked(types.Notify{Event: "profiles", Profiles: append([]types.Profile(nil), s.profiles...)})
	appdLog.Printf("profile created id=%s total=%d", profile.ID, len(s.profiles))
	return profile, nil
}

func (s *Service) UpdateProfile(id string, req types.ProfileUpdateRequest) (types.Profile, error) {
	appdLog.Printf("update profile start id=%s", id)
	appdDebugf("update profile request id=%s", id)
	s.mu.Lock()
	index := -1
	for i, p := range s.profiles {
		if p.ID == id {
			index = i
			break
		}
	}
	if index == -1 {
		s.mu.Unlock()
		return types.Profile{}, fmt.Errorf("profile not found: %s", id)
	}
	before := s.profiles[index]
	appdDebugf("update profile before id=%s name=%q server=%q accept=%v include=%d exclude=%d",
		before.ID, before.Name, before.ServerURL, before.AcceptServerRoutes, len(before.CustomInclude), len(before.CustomExclude))
	profile := before
	if req.Name != nil {
		profile.Name = *req.Name
	}
	if req.ServerURL != nil {
		profile.ServerURL = *req.ServerURL
	}
	if req.Username != nil {
		profile.Username = *req.Username
	}
	if req.Group != nil {
		profile.Group = *req.Group
	}
	if req.AcceptServerRoutes != nil {
		profile.AcceptServerRoutes = *req.AcceptServerRoutes
	}
	if req.AutoReconnect != nil {
		profile.AutoReconnect = req.AutoReconnect
	}
	if req.ApplyDNS != nil {
		profile.ApplyDNS = req.ApplyDNS
	}
	if req.CustomInclude != nil {
		profile.CustomInclude = append([]string(nil), req.CustomInclude...)
	}
	if req.CustomExclude != nil {
		profile.CustomExclude = append([]string(nil), req.CustomExclude...)
	}
	if req.DNSOverrides != nil {
		profile.DNSOverrides = append([]string(nil), req.DNSOverrides...)
	}
	if req.SOCKS5Enabled != nil {
		profile.SOCKS5Enabled = *req.SOCKS5Enabled
	}
	if req.SOCKS5Listen != nil {
		profile.SOCKS5Listen = *req.SOCKS5Listen
	}
	if req.MTU != nil {
		profile.MTU = *req.MTU
	}
	if req.Password != nil && profile.SecretRef != "" {
		if err := s.secrets.Put(profile.SecretRef, *req.Password); err != nil {
			s.mu.Unlock()
			return types.Profile{}, err
		}
	}
	profile = profileio.NormalizeProfile(profile)
	profile.UpdatedAt = now()
	s.profiles[index] = profile
	if err := s.persist(); err != nil {
		s.mu.Unlock()
		return types.Profile{}, err
	}
	shouldReconnect := s.connectedID == id && needsReconnectForProfileUpdate(before, profile)
	appdDebugf("update profile result id=%s should_reconnect=%v", id, shouldReconnect)
	if s.connectedID == id && s.status.Session != nil {
		s.status.EffectiveRoutes = s.planner.Plan(s.status.Session.SplitInclude, s.status.Session.SplitExclude, profile)
		s.status.UpdatedAt = now()
		s.emitLocked(types.Notify{Event: "status", Status: ptrStatus(s.status)})
	}
	s.logs.Add("info", fmt.Sprintf("appd: profile updated id=%s name=%q", profile.ID, profile.Name))
	appdLog.Printf("update profile done id=%s reconnect=%v", id, shouldReconnect)
	s.emitLocked(types.Notify{Event: "profile", Profile: &profile})
	s.emitLocked(types.Notify{Event: "profiles", Profiles: append([]types.Profile(nil), s.profiles...)})
	s.mu.Unlock()
	if err := s.applyProxyProfile(profile); err != nil {
		return profile, err
	}
	if shouldReconnect {
		if err := s.reconnectProfile(context.Background(), id, "reapplying updated profile "+id); err != nil {
			return profile, err
		}
	}
	return profile, nil
}

func (s *Service) DeleteProfile(id string) error {
	appdLog.Printf("delete profile start id=%s", id)
	wasConnected := false
	s.mu.Lock()
	index := -1
	for i, p := range s.profiles {
		if p.ID == id {
			index = i
			break
		}
	}
	if index == -1 {
		s.mu.Unlock()
		return fmt.Errorf("profile not found: %s", id)
	}
	if s.reconnectProfileID == id {
		s.stopReconnectLocked()
	}
	wasConnected = s.connectedID == id
	_ = s.secrets.Delete(s.profiles[index].SecretRef)
	s.profiles = append(s.profiles[:index], s.profiles[index+1:]...)
	if s.currentID == id {
		s.currentID = ""
		if len(s.profiles) == 1 {
			s.currentID = s.profiles[0].ID
		}
		s.status.CurrentProfileID = s.currentID
	}
	if wasConnected {
		s.connectedID = ""
		s.status.State = types.StateDisconnected
		s.status.ConnectedProfileID = ""
		s.status.Session = nil
		s.status.EffectiveRoutes = nil
		s.status.UpdatedAt = now()
	}
	if err := s.persist(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.logs.Add("info", fmt.Sprintf("appd: profile deleted id=%s", id))
	appdLog.Printf("delete profile done id=%s remaining=%d", id, len(s.profiles))
	if wasConnected {
		s.emitLocked(types.Notify{Event: "status", Status: ptrStatus(s.status)})
	}
	s.emitLocked(types.Notify{Event: "profiles", Profiles: append([]types.Profile(nil), s.profiles...)})
	s.mu.Unlock()
	if wasConnected {
		_ = s.stopProxy()
	}
	return nil
}

func (s *Service) SwitchProfile(_ context.Context, id string) error {
	appdLog.Printf("switch profile request to=%s", id)
	s.mu.Lock()
	_, err := s.findProfileLocked(id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if s.currentID != id {
		s.stopReconnectLocked()
	}
	s.currentID = id
	s.status.CurrentProfileID = id
	if err := s.persist(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.logs.Add("info", fmt.Sprintf("appd: switched current profile id=%s", id))
	appdLog.Printf("switch profile done current=%s", id)
	s.emitLocked(types.Notify{Event: "status", Status: ptrStatus(s.status)})
	s.mu.Unlock()
	return nil
}

func (s *Service) Connect(ctx context.Context, id string) error {
	appdDebugf("connect start profile=%s current=%s connected=%s", id, s.status.CurrentProfileID, s.connectedID)
	s.mu.Lock()
	s.stopReconnectLocked()
	profile, err := s.findProfileLocked(id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	password, _ := s.secrets.Get(profile.SecretRef)
	s.mu.Unlock()
	connectStart := time.Now()
	if err := s.connectPreparedProfile(ctx, profile, password, false); err != nil {
		appdLog.Printf("connect failed profile=%s err=%v", profile.ID, err)
		return err
	}
	s.mu.Lock()
	appdLog.Printf("connect success profile=%s state=%s", profile.ID, s.status.State)
	appdDebugf("connect success profile=%s duration=%s routes=%d", profile.ID, time.Since(connectStart), len(s.status.EffectiveRoutes))
	s.mu.Unlock()
	return nil
}

func (s *Service) ConnectCurrent(ctx context.Context) error {
	s.mu.Lock()
	currentID := s.currentID
	s.mu.Unlock()
	if currentID == "" {
		return errNoCurrentProfile
	}
	return s.Connect(ctx, currentID)
}

func (s *Service) Disconnect(ctx context.Context) error {
	return s.disconnect(ctx, true)
}

func (s *Service) disconnect(ctx context.Context, manual bool) error {
	s.mu.Lock()
	appdDebugf("disconnect requested current_connected=%s", s.connectedID)
	connected := s.connectedID != ""
	if manual {
		s.stopReconnectLocked()
	}
	if manual && connected {
		s.disconnectSeq++
		s.manualDisconnectSeq = s.disconnectSeq
		s.manualProfileID = s.connectedID
	}
	s.mu.Unlock()
	if !connected {
		return nil
	}
	if err := s.backend.Disconnect(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.connectedID = ""
	s.status.State = types.StateDisconnected
	s.status.ConnectedProfileID = ""
	s.status.Session = nil
	s.status.EffectiveRoutes = nil
	s.status.LastError = ""
	s.status.UpdatedAt = now()
	s.logs.Add("info", fmt.Sprintf("appd: profile disconnected id=%s", s.status.CurrentProfileID))
	appdLog.Printf("disconnect done previous_profile=%s", s.status.CurrentProfileID)
	s.emitLocked(types.Notify{
		Event:   "status",
		Status:  ptrStatus(s.status),
		Message: "Disconnected.",
	})
	s.mu.Unlock()
	_ = s.stopProxy()
	return nil
}

func (s *Service) UpdateRoutes(id string, req types.RouteUpdateRequest) (types.Profile, error) {
	appdLog.Printf("update routes start id=%s", id)
	appdDebugf("update routes request id=%s accept=%v include=%v exclude=%v", id, req.AcceptServerRoutes, req.CustomInclude, req.CustomExclude)
	s.mu.Lock()
	index := -1
	for i, p := range s.profiles {
		if p.ID == id {
			index = i
			break
		}
	}
	if index == -1 {
		s.mu.Unlock()
		return types.Profile{}, fmt.Errorf("profile not found: %s", id)
	}
	if req.AcceptServerRoutes != nil {
		s.profiles[index].AcceptServerRoutes = *req.AcceptServerRoutes
	}
	if req.CustomInclude != nil {
		s.profiles[index].CustomInclude = append([]string(nil), req.CustomInclude...)
	}
	if req.CustomExclude != nil {
		s.profiles[index].CustomExclude = append([]string(nil), req.CustomExclude...)
	}
	s.profiles[index].UpdatedAt = now()
	if err := s.persist(); err != nil {
		s.mu.Unlock()
		return types.Profile{}, err
	}
	profile := s.profiles[index]
	shouldReconnect := s.connectedID == id
	appdDebugf("update routes result id=%s should_reconnect=%v include=%d exclude=%d",
		id, shouldReconnect, len(profile.CustomInclude), len(profile.CustomExclude))
	if s.connectedID == id && s.status.Session != nil {
		s.status.EffectiveRoutes = s.planner.Plan(s.status.Session.SplitInclude, s.status.Session.SplitExclude, profile)
		s.status.UpdatedAt = now()
		s.emitLocked(types.Notify{Event: "status", Status: ptrStatus(s.status)})
	}
	s.logs.Add("info", fmt.Sprintf("appd: routes updated profile=%s include_count=%d exclude_count=%d", id, len(req.CustomInclude), len(req.CustomExclude)))
	appdLog.Printf("update routes done id=%s reconnect=%v", id, shouldReconnect)
	s.emitLocked(types.Notify{Event: "profile", Profile: &profile})
	s.mu.Unlock()
	if shouldReconnect {
		if err := s.reconnectProfile(context.Background(), id, "reapplying route changes for profile "+id); err != nil {
			return profile, err
		}
	}
	return profile, nil
}

func (s *Service) Login(ctx context.Context, req types.LoginRequest) error {
	appdLog.Printf("start login interactive profile=%s server=%q", req.ProfileID, req.ServerURL)
	appdDebugf("start login interactive profile=%s", req.ProfileID)
	s.mu.Lock()
	profile, password, err := s.prepareLoginProfileLocked(req)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.persist(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.logs.Add("info", fmt.Sprintf("appd: prepared login profile id=%s", profile.ID))
	s.emitLocked(types.Notify{Event: "profile", Profile: &profile})
	s.emitLocked(types.Notify{Event: "profiles", Profiles: append([]types.Profile(nil), s.profiles...)})
	s.mu.Unlock()
	return s.connectPreparedProfile(ctx, profile, password, false)
}

func (s *Service) Watch(ctx context.Context) <-chan types.Notify {
	appdLog.Printf("watch client subscribed")
	appdDebugf("watch subscribed state=%s profile_count=%d", s.status.State, len(s.profiles))
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan types.Notify, 16)
	id := s.nextWatcherID
	s.nextWatcherID++
	s.watchers[id] = ch
	ch <- types.Notify{
		Version:  version,
		Event:    "snapshot",
		Status:   ptrStatus(s.status),
		Profiles: append([]types.Profile(nil), s.profiles...),
		Time:     now(),
	}
	go func() {
		<-ctx.Done()
		appdDebugf("watch context done id=%d", id)
		s.mu.Lock()
		delete(s.watchers, id)
		close(ch)
		s.mu.Unlock()
	}()
	return ch
}

func (s *Service) consumeBackendEvents() {
	appdDebugf("backend event loop started")
	for event := range s.backend.Events() {
		appdLog.Printf("backend event type=%s", event.Type)
		appdDebugf("backend event type=%s err=%v session=%+v", event.Type, event.Err, event.Session)
		s.mu.Lock()
		var disconnectedProfileID string
		var scheduleAutoReconnect bool
		var manualSeq uint64
		switch event.Type {
		case "connected":
			if s.currentID != "" {
				s.connectedID = s.currentID
				s.status.ConnectedProfileID = s.currentID
			}
			s.status.State = types.StateConnected
			s.status.Session = event.Session
			s.logs.Add("info", "appd: backend event=connected")
		case "disconnected":
			disconnectedProfileID = s.connectedID
			autoProfile, autoProfileFound := s.profileAutoReconnect(disconnectedProfileID)
			manual := disconnectedProfileID != "" && disconnectedProfileID == s.manualProfileID && s.manualDisconnectSeq == s.disconnectSeq
			if manual {
				s.manualProfileID = ""
				s.manualDisconnectSeq = 0
			}
			if !manual && s.currentID == disconnectedProfileID &&
				autoProfileFound && types.BoolValue(autoProfile.AutoReconnect, false) &&
				s.reconnectTimer == nil {
				manualSeq = s.disconnectSeq
				scheduleAutoReconnect = true
			}
			s.connectedID = ""
			s.status.State = types.StateDisconnected
			s.status.ConnectedProfileID = ""
			s.status.Session = nil
			s.status.EffectiveRoutes = nil
			s.status.SOCKS5Enabled = false
			s.status.SOCKS5Listen = ""
			s.logs.Add("info", "appd: backend event=disconnected")
		}
		message := ""
		switch event.Type {
		case "connected":
			message = "Backend connection established."
		case "disconnected":
			message = "Backend disconnected."
		}
		if event.Err != nil {
			appdLog.Printf("backend event error: %v", event.Err)
			s.status.State = types.StateError
			s.status.LastError = event.Err.Error()
			s.logs.Add("error", fmt.Sprintf("appd: backend error err=%q", event.Err.Error()))
			message = "Backend error: " + event.Err.Error()
		}
		s.status.UpdatedAt = now()
		s.emitLocked(types.Notify{
			Event:   "status",
			Status:  ptrStatus(s.status),
			Error:   s.status.LastError,
			Message: message,
		})
		s.mu.Unlock()
		if scheduleAutoReconnect {
			s.mu.Lock()
			s.startReconnectLocked(disconnectedProfileID, manualSeq, 1)
			s.mu.Unlock()
		}
	}
}

func (s *Service) profileAutoReconnect(profileID string) (types.Profile, bool) {
	if profileID == "" {
		return types.Profile{}, false
	}
	for _, profile := range s.profiles {
		if profile.ID == profileID {
			return profile, true
		}
	}
	return types.Profile{}, false
}

func (s *Service) startReconnectLocked(profileID string, manualSeq uint64, attempt int) {
	profile, ok := s.profileAutoReconnect(profileID)
	if !ok {
		return
	}
	if s.currentID != profileID || s.connectedID != "" || s.disconnectSeq != manualSeq ||
		!types.BoolValue(profile.AutoReconnect, false) {
		return
	}
	if attempt < 1 {
		attempt = 1
	}
	if s.reconnectTimer != nil {
		s.reconnectTimer.Stop()
	}
	delay := reconnectDelay(attempt)
	s.reconnectID++
	reconnectID := s.reconnectID
	s.reconnectTimer = time.AfterFunc(delay, func() {
		s.runScheduledReconnect(profileID, manualSeq, attempt, reconnectID)
	})
	s.reconnectProfileID = profileID
	s.reconnectAttempt = attempt
	s.reconnectSeq = manualSeq
	s.logs.Add("info", fmt.Sprintf("appd: auto reconnect scheduled id=%q attempt=%d delay=%s", profileID, attempt, delay))
	appdLog.Printf("auto reconnect scheduled id=%q attempt=%d delay=%s", profileID, attempt, delay)
}

func (s *Service) runScheduledReconnect(profileID string, manualSeq uint64, attempt int, reconnectID uint64) {
	s.mu.Lock()
	if s.reconnectID != reconnectID || s.reconnectProfileID != profileID || s.reconnectSeq != manualSeq {
		s.mu.Unlock()
		return
	}
	s.reconnectTimer = nil
	profile, ok := s.profileAutoReconnect(profileID)
	if !ok || s.currentID != profileID || s.connectedID != "" || s.disconnectSeq != manualSeq ||
		!types.BoolValue(profile.AutoReconnect, false) {
		s.stopReconnectLocked()
		s.mu.Unlock()
		return
	}
	s.status.State = types.StateReconnecting
	s.status.LastError = ""
	s.status.UpdatedAt = now()
	s.logs.Add("info", fmt.Sprintf("appd: auto reconnect attempt=%d id=%q", attempt, profileID))
	s.emitLocked(types.Notify{
		Event:   "status",
		Status:  ptrStatus(s.status),
		Message: "Reconnecting profile " + profileID,
	})
	s.mu.Unlock()

	s.logs.Add("info", fmt.Sprintf("appd: reconnect reason=%q", fmt.Sprintf("auto reconnect attempt %d for profile %s", attempt, profileID)))
	if err := s.disconnect(context.Background(), false); err != nil {
		s.mu.Lock()
		s.logs.Add("error", fmt.Sprintf("appd: auto reconnect failed id=%q err=%q", profileID, err.Error()))
		appdLog.Printf("auto reconnect failed id=%q attempt=%d err=%v", profileID, attempt, err)
		s.startReconnectLocked(profileID, manualSeq, attempt+1)
		s.mu.Unlock()
		return
	}
	password, _ := s.secrets.Get(profile.SecretRef)
	err := s.connectPreparedProfile(context.Background(), profile, password, true)

	s.mu.Lock()
	if err != nil {
		s.logs.Add("error", fmt.Sprintf("appd: auto reconnect failed id=%q err=%q", profileID, err.Error()))
		appdLog.Printf("auto reconnect failed id=%q attempt=%d err=%v", profileID, attempt, err)
		s.startReconnectLocked(profileID, manualSeq, attempt+1)
		s.mu.Unlock()
		return
	}
	s.stopReconnectLocked()
	s.mu.Unlock()
}

func (s *Service) stopReconnectLocked() {
	if s.reconnectTimer != nil {
		s.reconnectTimer.Stop()
	}
	s.reconnectTimer = nil
	s.reconnectProfileID = ""
	s.reconnectAttempt = 0
	s.reconnectSeq = 0
	s.reconnectID++
}

func reconnectDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := autoReconnectMinDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= autoReconnectMaxDelay {
			return autoReconnectMaxDelay
		}
	}
	if delay > autoReconnectMaxDelay {
		return autoReconnectMaxDelay
	}
	return delay
}

func (s *Service) findProfileLocked(id string) (types.Profile, error) {
	for _, p := range s.profiles {
		if p.ID == id {
			return p, nil
		}
	}
	return types.Profile{}, fmt.Errorf("profile not found: %s", id)
}

func (s *Service) emitLocked(notify types.Notify) {
	notify.Version = version
	notify.Time = now()
	appdDebugf("emit notify event=%s watchers=%d", notify.Event, len(s.watchers))
	for _, ch := range s.watchers {
		select {
		case ch <- notify:
		default:
			appdDebugf("emit notify dropped event=%s", notify.Event)
		}
	}
}

func ptrStatus(status types.Status) *types.Status {
	copy := status
	return &copy
}

func (s *Service) reconnectProfile(ctx context.Context, id, reason string) error {
	appdDebugf("reconnect profile id=%s reason=%s", id, reason)
	s.logs.Add("info", fmt.Sprintf("appd: reconnect reason=%q", reason))
	if err := s.disconnect(ctx, false); err != nil {
		return err
	}
	return s.Connect(ctx, id)
}

func needsReconnectForProfileUpdate(before, after types.Profile) bool {
	if before.ServerURL != after.ServerURL ||
		before.Username != after.Username ||
		before.Group != after.Group ||
		before.AcceptServerRoutes != after.AcceptServerRoutes ||
		types.BoolValue(before.AutoReconnect, false) != types.BoolValue(after.AutoReconnect, false) ||
		types.BoolValue(before.ApplyDNS, true) != types.BoolValue(after.ApplyDNS, true) ||
		before.MTU != after.MTU {
		return true
	}
	if !slices.Equal(before.CustomInclude, after.CustomInclude) ||
		!slices.Equal(before.CustomExclude, after.CustomExclude) ||
		!slices.Equal(before.DNSOverrides, after.DNSOverrides) {
		return true
	}
	return false
}

func (s *Service) prepareLoginProfileLocked(req types.LoginRequest) (types.Profile, string, error) {
	var (
		profile  types.Profile
		password string
		index    = -1
	)
	if req.ProfileID != "" {
		for i, p := range s.profiles {
			if p.ID == req.ProfileID {
				index = i
				profile = p
				break
			}
		}
		if index == -1 {
			return types.Profile{}, "", fmt.Errorf("profile not found: %s", req.ProfileID)
		}
	} else {
		profile = types.NewProfile(req.Name)
		profile.SecretRef = "profile/" + profile.ID
	}

	if req.Name != "" {
		profile.Name = req.Name
	}
	if req.ServerURL != "" {
		profile.ServerURL = req.ServerURL
	}
	if req.Username != "" {
		profile.Username = req.Username
	}
	if req.Group != "" {
		profile.Group = req.Group
	}
	profile = profileio.NormalizeProfile(profile)
	if profile.Name == "" {
		profile.Name = profileio.DefaultProfileName(profile.ServerURL, profile.Username)
	}
	if profile.SecretRef == "" {
		profile.SecretRef = "profile/" + profile.ID
	}
	profile.UpdatedAt = now()
	if profile.CreatedAt == "" {
		profile.CreatedAt = profile.UpdatedAt
	}
	if req.Password != "" {
		password = req.Password
		if err := s.secrets.Put(profile.SecretRef, req.Password); err != nil {
			return types.Profile{}, "", err
		}
	} else {
		password, _ = s.secrets.Get(profile.SecretRef)
	}

	if index >= 0 {
		s.profiles[index] = profile
	} else {
		s.profiles = append(s.profiles, profile)
	}
	s.currentID = profile.ID
	s.status.CurrentProfileID = profile.ID
	return profile, password, nil
}

func (s *Service) connectPreparedProfile(ctx context.Context, profile types.Profile, password string, allowReconnectState bool) error {
	if err := validateProfileForConnect(profile); err != nil {
		return err
	}
	if profile.SecretRef != "" && password == "" {
		password, _ = s.secrets.Get(profile.SecretRef)
	}

	s.mu.Lock()
	if s.status.State == types.StateConnecting || s.status.State == types.StateReconnecting {
		if !s.connectAllowedFromStateLocked(profile.ID, allowReconnectState) && s.status.CurrentProfileID == profile.ID {
			appdLog.Printf("connect ignored profile=%s reason=already_connecting", profile.ID)
			s.mu.Unlock()
			return nil
		}
		if !s.connectAllowedFromStateLocked(profile.ID, allowReconnectState) {
			s.mu.Unlock()
			return errConnectInProgress
		}
	}
	if s.status.State == types.StateConnected && s.connectedID == profile.ID {
		appdLog.Printf("connect ignored profile=%s reason=already_connected", profile.ID)
		s.mu.Unlock()
		return nil
	}
	alreadyConnected := s.connectedID
	s.mu.Unlock()
	if alreadyConnected != "" && alreadyConnected != profile.ID {
		if err := s.disconnect(ctx, false); err != nil {
			return err
		}
	}

	s.mu.Lock()
	if s.status.State == types.StateConnecting || s.status.State == types.StateReconnecting {
		if !s.connectAllowedFromStateLocked(profile.ID, allowReconnectState) && s.status.CurrentProfileID == profile.ID {
			appdLog.Printf("connect ignored profile=%s reason=already_connecting", profile.ID)
			s.mu.Unlock()
			return nil
		}
		if !s.connectAllowedFromStateLocked(profile.ID, allowReconnectState) {
			s.mu.Unlock()
			return errConnectInProgress
		}
	}
	if s.status.State == types.StateConnected && s.connectedID == profile.ID {
		appdLog.Printf("connect ignored profile=%s reason=already_connected", profile.ID)
		s.mu.Unlock()
		return nil
	}
	if allowReconnectState {
		s.status.State = types.StateReconnecting
	} else {
		s.status.State = types.StateConnecting
	}
	s.currentID = profile.ID
	s.status.CurrentProfileID = profile.ID
	s.status.ConnectedProfileID = ""
	s.status.LastError = ""
	s.status.UpdatedAt = now()
	s.logs.Add("info", fmt.Sprintf("appd: connecting id=%s server=%q", profile.ID, profile.ServerURL))
	s.emitLocked(types.Notify{
		Event:   "status",
		Status:  ptrStatus(s.status),
		Message: "Connecting to profile " + profile.ID,
	})
	s.mu.Unlock()

	session, err := s.backend.Connect(ctx, profile, password)
	if err != nil {
		s.mu.Lock()
		s.status.State = types.StateError
		s.status.LastError = err.Error()
		s.status.UpdatedAt = now()
		s.logs.Add("error", fmt.Sprintf("appd: connect failed id=%s err=%q", profile.ID, err.Error()))
		s.emitLocked(types.Notify{
			Event:   "status",
			Status:  ptrStatus(s.status),
			Error:   err.Error(),
			Message: "Failed to connect profile " + profile.ID + ": " + err.Error(),
		})
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	s.connectedID = profile.ID
	s.status.State = types.StateConnected
	s.status.ConnectedProfileID = profile.ID
	s.status.Session = session
	s.status.EffectiveRoutes = s.planner.Plan(session.SplitInclude, session.SplitExclude, profile)
	s.status.UpdatedAt = now()
	s.logs.Add("info", fmt.Sprintf("appd: connected profile=%s", profile.ID))
	s.emitLocked(types.Notify{
		Event:   "status",
		Status:  ptrStatus(s.status),
		Message: "Connected to profile " + profile.ID,
	})
	s.mu.Unlock()
	return s.applyProxyProfile(profile)
}

func (s *Service) connectAllowedFromStateLocked(profileID string, allowReconnectState bool) bool {
	return allowReconnectState && s.status.State == types.StateReconnecting && s.status.CurrentProfileID == profileID
}

func validateProfileForConnect(profile types.Profile) error {
	if profile.ServerURL == "" {
		return errors.New("profile server_url is required")
	}
	if profile.Username == "" {
		return errors.New("profile username is required")
	}
	return nil
}

func (s *Service) applyProxyProfile(profile types.Profile) error {
	appdLog.Printf("apply proxy for profile=%s enabled=%v addr=%q", profile.ID, profile.SOCKS5Enabled, profile.SOCKS5Listen)
	s.mu.Lock()
	connected := s.connectedID == profile.ID && s.status.State == types.StateConnected
	s.mu.Unlock()
	if !connected {
		return s.stopProxy()
	}
	if !profile.SOCKS5Enabled {
		return s.stopProxy()
	}
	return s.startProxy(profile.SOCKS5Listen)
}

func (s *Service) startProxy(listenAddr string) error {
	if listenAddr == "" {
		listenAddr = "127.0.0.1:1080"
	}
	appdLog.Printf("start proxy addr=%s", listenAddr)
	s.mu.Lock()
	if s.proxyServer != nil && s.status.SOCKS5Enabled && s.status.SOCKS5Listen == listenAddr {
		s.mu.Unlock()
		return nil
	}
	oldProxy := s.proxyServer
	s.proxyServer = nil
	s.status.SOCKS5Enabled = false
	s.status.SOCKS5Listen = ""
	s.mu.Unlock()

	if oldProxy != nil {
		_ = oldProxy.Close()
	}
	server, err := socks5.Listen(listenAddr)
	if err != nil {
		s.mu.Lock()
		s.logs.Add("error", fmt.Sprintf("appd: start socks5 proxy failed err=%q", err.Error()))
		appdLog.Printf("start proxy failed addr=%s err=%v", listenAddr, err)
		s.status.LastError = err.Error()
		s.status.UpdatedAt = now()
		s.emitLocked(types.Notify{Event: "status", Status: ptrStatus(s.status), Error: err.Error()})
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	s.proxyServer = server
	s.status.SOCKS5Enabled = true
	s.status.SOCKS5Listen = server.Addr()
	s.status.UpdatedAt = now()
	s.logs.Add("info", fmt.Sprintf("appd: socks5 proxy started addr=%s", server.Addr()))
	appdLog.Printf("start proxy success listen=%s", server.Addr())
	s.emitLocked(types.Notify{Event: "status", Status: ptrStatus(s.status), Message: "SOCKS5 proxy started"})
	s.mu.Unlock()
	return nil
}

func (s *Service) stopProxy() error {
	s.mu.Lock()
	server := s.proxyServer
	if server == nil && !s.status.SOCKS5Enabled && s.status.SOCKS5Listen == "" {
		s.mu.Unlock()
		return nil
	}
	s.proxyServer = nil
	wasEnabled := s.status.SOCKS5Enabled
	oldAddr := s.status.SOCKS5Listen
	s.status.SOCKS5Enabled = false
	s.status.SOCKS5Listen = ""
	s.status.UpdatedAt = now()
	if wasEnabled {
		s.logs.Add("info", fmt.Sprintf("appd: socks5 proxy stopped addr=%s", oldAddr))
		appdLog.Printf("stop proxy done addr=%s", oldAddr)
		s.emitLocked(types.Notify{Event: "status", Status: ptrStatus(s.status), Message: "SOCKS5 proxy stopped"})
	}
	s.mu.Unlock()
	if server != nil {
		return server.Close()
	}
	return nil
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
