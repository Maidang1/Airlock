package core

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Snapshot struct {
	Booting             bool
	BootError           string
	Connected           bool
	Locked              bool
	LastRefreshAt       time.Time
	Context             ClientContext
	LocalStatus         DesktopLocalStatus
	Account             AccountView
	AccountLoaded       bool
	Init                InitializationView
	InitLoaded          bool
	Status              StatusView
	StatusLoaded        bool
	Settings            SettingsView
	Appearance          Appearance
	Proxy               ProxyStatusView
	BrowserTabs         BrowserTabsView
	Terminals           []TerminalSummary
	CLISessions         []CLISessionSummary
	ClaudePanel         ClaudePanelView
	ClaudeDesktop       ClaudeDesktopState
	Tailscale           TailnetStatus
	Hosts               []HostProfile
	Vault               VaultData
	Favorites           Favorites
	MobilePair          MobilePairStatus
	Diagnostics         DiagnosticsInfo
	About               AboutCheck
	Build               BuildInfo
	Resources           ResourceStatus
	DesktopFileRequests []DesktopFileRequest
	SavedDownloads      []BrowserDownload
	Update              map[string]any
	ClientPrefs         ClientPreferences
	LastError           string
}

type Store struct {
	client *ControllerClient

	mu           sync.RWMutex
	snapshot     Snapshot
	invalidate   func()
	onToast      func(level, message string)
	onPrefs      func(ClientPreferences)
	onUnlocked   func()
	cancel              context.CancelFunc
	refreshCh           chan struct{}
	browserSSEMu        sync.Mutex
	sseCancel           context.CancelFunc
	defaultLocalTried   bool
	stayDisconnectedGen string
}

func DefaultClientPreferences() ClientPreferences {
	return ClientPreferences{
		AudioMuted:    false,
		AudioVolume:   1.0,
		ThemeMode:     "system",
		SidebarWidth:  260,
		TerminalSplit: 0.35,
	}
}

func NewStore(client *ControllerClient) *Store {
	s := &Store{
		client:    client,
		refreshCh: make(chan struct{}, 4),
		snapshot: Snapshot{
			Booting:     true,
			ClientPrefs: DefaultClientPreferences(),
		},
	}
	s.loadClientPreferences()
	client.SetOnLocked(func() {
		s.mu.Lock()
		s.snapshot.Locked = true
		s.snapshot.Account.Unlocked = false
		s.mu.Unlock()
		s.notify()
	})
	return s
}

func (s *Store) Client() *ControllerClient {
	return s.client
}

func (s *Store) SetInvalidate(fn func()) {
	s.mu.Lock()
	s.invalidate = fn
	s.mu.Unlock()
}

func (s *Store) SetOnToast(fn func(level, message string)) {
	s.mu.Lock()
	s.onToast = fn
	s.mu.Unlock()
}

func (s *Store) SetOnPrefsChanged(fn func(ClientPreferences)) {
	s.mu.Lock()
	s.onPrefs = fn
	s.mu.Unlock()
}

func (s *Store) SetOnUnlocked(fn func()) {
	s.mu.Lock()
	s.onUnlocked = fn
	s.mu.Unlock()
}

func (s *Store) notify() {
	s.mu.RLock()
	fn := s.invalidate
	s.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

func (s *Store) Toast(level, message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	s.mu.RLock()
	fn := s.onToast
	s.mu.RUnlock()
	if fn != nil {
		fn(level, message)
	}
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.snapshot
	out.Status.Projects = append([]string(nil), s.snapshot.Status.Projects...)
	out.Status.ProjectGroups = append([]ProjectGroup(nil), s.snapshot.Status.ProjectGroups...)
	out.BrowserTabs.Tabs = append([]BrowserTab(nil), s.snapshot.BrowserTabs.Tabs...)
	out.BrowserTabs.Uploads = append([]BrowserUpload(nil), s.snapshot.BrowserTabs.Uploads...)
	out.BrowserTabs.Downloads = append([]BrowserDownload(nil), s.snapshot.BrowserTabs.Downloads...)
	out.Terminals = append([]TerminalSummary(nil), s.snapshot.Terminals...)
	out.CLISessions = append([]CLISessionSummary(nil), s.snapshot.CLISessions...)
	out.Hosts = append([]HostProfile(nil), s.snapshot.Hosts...)
	out.ClaudePanel.Accounts = append([]ClaudeAccount(nil), s.snapshot.ClaudePanel.Accounts...)
	out.Proxy.Sources = append([]ProxySource(nil), s.snapshot.Proxy.Sources...)
	out.Proxy.Route = append([]string(nil), s.snapshot.Proxy.Route...)
	out.Tailscale.Peers = append([]TailnetPeer(nil), s.snapshot.Tailscale.Peers...)
	out.Tailscale.Keys = append([]string(nil), s.snapshot.Tailscale.Keys...)
	out.Vault.Entries = append([]VaultEntry(nil), s.snapshot.Vault.Entries...)
	out.Vault.Blocked = append([]string(nil), s.snapshot.Vault.Blocked...)
	out.Favorites.Items = append([]Favorite(nil), s.snapshot.Favorites.Items...)
	out.Favorites.Folders = append([]FavoriteFolder(nil), s.snapshot.Favorites.Folders...)
	out.DesktopFileRequests = append([]DesktopFileRequest(nil), s.snapshot.DesktopFileRequests...)
	out.SavedDownloads = append([]BrowserDownload(nil), s.snapshot.SavedDownloads...)
	out.Update = maps.Clone(s.snapshot.Update)
	return out
}

func (s *Store) prefsPath() string {
	dir := s.client.ProfileDir()
	if dir == "" || dir == "." {
		return ""
	}
	return filepath.Join(dir, "client-preferences.json")
}

func (s *Store) loadClientPreferences() {
	path := s.prefsPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	prefs := DefaultClientPreferences()
	if json.Unmarshal(data, &prefs) == nil {
		if prefs.SidebarWidth < 180 || prefs.SidebarWidth > 480 {
			prefs.SidebarWidth = 260
		}
		if prefs.ThemeMode == "" {
			prefs.ThemeMode = "system"
		}
		if prefs.AudioVolume <= 0 || prefs.AudioVolume > 1 {
			prefs.AudioVolume = 1.0
		}
		s.mu.Lock()
		s.snapshot.ClientPrefs = prefs
		s.mu.Unlock()
	}
}

func (s *Store) UpdateClientPreferences(update func(*ClientPreferences)) {
	s.mu.Lock()
	prefs := s.snapshot.ClientPrefs
	update(&prefs)
	if prefs.ThemeMode == "" {
		prefs.ThemeMode = "system"
	}
	if prefs.SidebarWidth < 180 {
		prefs.SidebarWidth = 180
	}
	if prefs.SidebarWidth > 480 {
		prefs.SidebarWidth = 480
	}
	if prefs.AudioVolume <= 0 {
		prefs.AudioVolume = 1.0
	}
	s.snapshot.ClientPrefs = prefs
	cb := s.onPrefs
	s.mu.Unlock()

	if path := s.prefsPath(); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err == nil {
			if data, err := json.MarshalIndent(prefs, "", "  "); err == nil {
				tmp := path + ".tmp"
				if os.WriteFile(tmp, data, 0600) == nil {
					_ = os.Rename(tmp, path)
				}
			}
		}
	}
	if cb != nil {
		cb(prefs)
	}
	s.notify()
}

func (s *Store) SetUpdateState(st map[string]any) {
	cloned := make(map[string]any, len(st))
	for k, v := range st {
		cloned[k] = v
	}
	s.mu.Lock()
	s.snapshot.Update = cloned
	s.mu.Unlock()
	s.notify()
}

func (s *Store) SetBootState(booting bool, bootErr string) {
	s.mu.Lock()
	s.snapshot.Booting = booting
	s.snapshot.BootError = bootErr
	s.mu.Unlock()
	s.notify()
}

func (s *Store) TriggerRefresh() {
	s.mu.RLock()
	running := s.cancel != nil
	s.mu.RUnlock()
	if !running {
		go s.RefreshAll(context.Background())
		return
	}
	select {
	case s.refreshCh <- struct{}{}:
	default:
	}
}

func (s *Store) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	go s.runLoop(ctx)
}

func (s *Store) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.stopBrowserSSE()
}

func (s *Store) runLoop(ctx context.Context) {
	if err := s.BootstrapAndRefresh(ctx); err != nil {
		// Error is stored in Snapshot.BootError; wait for manual retry or shutdown.
	}
	for {
		delay := s.nextPollInterval()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.refreshCh:
			timer.Stop()
			s.RefreshAll(ctx)
		case <-timer.C:
			s.RefreshAll(ctx)
		}
	}
}

func (s *Store) nextPollInterval() time.Duration {
	s.mu.RLock()
	snap := s.snapshot
	s.mu.RUnlock()
	if !snap.Connected {
		return 4 * time.Second
	}
	if snap.Status.Job.Running || snap.Init.Required || snap.Context.Candidate != nil || snap.Context.State.State == "connecting" {
		return 750 * time.Millisecond
	}
	return 2500 * time.Millisecond
}

func (s *Store) MarkStayDisconnected(gen string) {
	s.mu.Lock()
	s.stayDisconnectedGen = strings.TrimSpace(gen)
	s.mu.Unlock()
}

func (s *Store) ConnectGatewayLocal(ctx context.Context) error {
	var cur ClientContext
	if err := s.client.GetJSON(ctx, "/api/client/context", &cur); err != nil {
		return err
	}
	var cand DesktopCandidate
	if err := s.client.PostJSON(ctx, "/api/client/candidate", map[string]any{
		"connection_id": cur.ConnectionID,
		"mode":          "local",
	}, &cand); err != nil {
		return err
	}
	deadline := time.Now().Add(20 * time.Second)
	for cand.Connection.State != "connected" {
		if cand.Connection.State == "failed" || cand.Connection.State == "disconnected" {
			msg := cand.Connection.Message
			if msg == "" {
				msg = "无法连接到本机环境"
			}
			return errors.New(msg)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("连接本机环境超时（%s）", cand.Connection.State)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		var polled ClientContext
		if err := s.client.GetJSON(ctx, "/api/client/context", &polled); err != nil {
			continue
		}
		if polled.Candidate == nil || polled.Candidate.ID != cand.ID {
			if polled.Mode == "local" && polled.State.State == "connected" {
				if polled.ConnectionID != "" {
					s.client.SetConnectionID(polled.ConnectionID)
				}
				return nil
			}
			return errors.New("连接候选已失效或被取消")
		}
		cand = *polled.Candidate
	}
	if err := s.client.PostJSON(ctx, "/api/client/commit", map[string]any{
		"connection_id": cur.ConnectionID,
		"candidate_id":  cand.ID,
		"local_action":  "auto",
	}, nil); err != nil {
		return err
	}
	var updated ClientContext
	if err := s.client.GetJSON(ctx, "/api/client/context", &updated); err == nil && updated.ConnectionID != "" {
		s.client.SetConnectionID(updated.ConnectionID)
	}
	return nil
}

func (s *Store) BootstrapAndRefresh(ctx context.Context) error {
	s.SetBootState(true, "")
	if err := s.client.Bootstrap(ctx); err != nil {
		s.mu.Lock()
		s.snapshot.Booting = false
		s.snapshot.BootError = err.Error()
		s.snapshot.Connected = false
		s.mu.Unlock()
		s.notify()
		return err
	}
	s.mu.Lock()
	s.snapshot.Booting = false
	s.snapshot.BootError = ""
	s.snapshot.Connected = true
	s.mu.Unlock()
	s.RefreshAll(ctx)
	return nil
}

func (s *Store) RefreshAll(ctx context.Context) {
	s.mu.RLock()
	connected := s.snapshot.Connected
	s.mu.RUnlock()
	if !connected {
		return
	}

	var (
		clientCtx   ClientContext
		localStatus DesktopLocalStatus
		hostsResp   struct {
			Hosts []HostProfile `json:"hosts"`
		}
		ctxErr error
	)
	if err := s.client.GetJSON(ctx, "/api/client/context", &clientCtx); err == nil {
		ctxErr = nil
		if clientCtx.ConnectionID != "" && clientCtx.ConnectionID != s.client.ConnectionID() {
			s.client.SetConnectionID(clientCtx.ConnectionID)
		}
		if clientCtx.DesktopGateway && !clientCtx.ClientOnly && clientCtx.Candidate == nil &&
			(clientCtx.Mode == "none" || (clientCtx.Mode == "local" && clientCtx.State.State == "failed")) {
			s.mu.Lock()
			tryLocal := (!s.defaultLocalTried || (clientCtx.Mode == "local" && clientCtx.State.State == "failed")) &&
				s.stayDisconnectedGen != clientCtx.ConnectionID
			if tryLocal {
				s.defaultLocalTried = true
			}
			s.mu.Unlock()
			if tryLocal {
				if err := s.ConnectGatewayLocal(ctx); err == nil {
					if s.client.GetJSON(ctx, "/api/client/context", &clientCtx) == nil && clientCtx.ConnectionID != "" {
						s.client.SetConnectionID(clientCtx.ConnectionID)
					}
				}
			}
		}
	} else {
		ctxErr = err
	}
	_ = s.client.GetJSON(ctx, "/api/client/local-status", &localStatus)
	_ = s.client.GetJSON(ctx, "/api/client/hosts", &hostsResp)

	var (
		account    AccountView
		accountErr error
	)
	accountErr = s.client.GetJSON(ctx, "/api/account", &account)

	var (
		initView   InitializationView
		initErr    error
		statusView StatusView
		statusErr  error
	)
	if accountErr == nil && (account.Unlocked || !account.Exists) {
		initErr = s.client.GetJSON(ctx, "/api/environment/initialization", &initView)
		statusErr = s.client.GetJSON(ctx, "/api/status", &statusView)
	}

	s.mu.Lock()
	prevUnlocked := s.snapshot.Account.Unlocked
	if ctxErr == nil {
		s.snapshot.Context = clientCtx
	}
	if localStatus.Known || localStatus.Available {
		s.snapshot.LocalStatus = localStatus
	}
	if hostsResp.Hosts != nil {
		s.snapshot.Hosts = hostsResp.Hosts
	}
	if accountErr == nil {
		s.snapshot.Account = account
		s.snapshot.AccountLoaded = true
		s.snapshot.Locked = account.Exists && !account.Unlocked
	}
	if initErr == nil {
		s.snapshot.Init = initView
		s.snapshot.InitLoaded = true
	}
	if statusErr == nil {
		s.snapshot.Status = statusView
		s.snapshot.StatusLoaded = true
	}
	s.snapshot.LastRefreshAt = time.Now()
	unlockedNow := s.snapshot.Account.Unlocked
	unlockedCb := s.onUnlocked
	s.mu.Unlock()

	if !prevUnlocked && unlockedNow && unlockedCb != nil {
		go unlockedCb()
	}

	if unlockedNow {
		s.refreshSecondary(ctx, statusView.Ready, statusView.VMRunning)
	}
	s.notify()
}

func (s *Store) refreshSecondary(ctx context.Context, ready, vmRunning bool) {
	var wg sync.WaitGroup
	var (
		settings   SettingsView
		appearance Appearance
		proxy      ProxyStatusView
		resources  ResourceStatus
		build      BuildInfo
		diag       DiagnosticsInfo
		mobile     MobilePairStatus
		favs       Favorites

		hasSettings, hasAppearance, hasProxy, hasResources, hasBuild, hasDiag, hasMobile, hasFavs bool
		proxyErr                                                                                error
	)

	fetch := func(path string, target any, flag *bool) {
		defer wg.Done()
		cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		if err := s.client.GetJSON(cctx, path, target); err == nil {
			*flag = true
		}
	}

	wg.Add(8)
	go fetch("/api/settings", &settings, &hasSettings)
	go fetch("/api/appearance", &appearance, &hasAppearance)
	go func() {
		defer wg.Done()
		cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		if err := s.client.GetJSON(cctx, "/api/proxy", &proxy); err == nil {
			hasProxy = true
		} else {
			proxyErr = err
		}
	}()
	go fetch("/api/environment/resources", &resources, &hasResources)
	go fetch("/api/client/about", &build, &hasBuild)
	go fetch("/api/diagnostics", &diag, &hasDiag)
	go fetch("/api/mobile-pair", &mobile, &hasMobile)
	go fetch("/api/favorites", &favs, &hasFavs)

	var (
		terminalsResp []TerminalSummary
		cliResp       struct {
			Sessions []CLISessionSummary `json:"sessions"`
		}
		claudePanel   ClaudePanelView
		claudeDesktop ClaudeDesktopState
		browserTabs   BrowserTabsView
		tailnet       TailnetStatus
		desktopReqs   struct {
			Requests []DesktopFileRequest `json:"requests"`
		}
		hasTerms, hasCLI, hasClaude, hasClaudeDesktop, hasTabs, hasTailnet, hasDesktopReqs bool
	)

	wg.Add(3)
	go fetch("/api/terminals", &terminalsResp, &hasTerms)
	go fetch("/api/cli/sessions", &cliResp, &hasCLI)
	go fetch("/api/claude-accounts", &claudePanel, &hasClaude)

	if vmRunning || ready {
		wg.Add(4)
		go fetch("/api/browser/tabs", &browserTabs, &hasTabs)
		go fetch("/api/claude-desktop", &claudeDesktop, &hasClaudeDesktop)
		go fetch("/api/tailscale", &tailnet, &hasTailnet)
		go fetch("/api/input/files/desktop/requests", &desktopReqs, &hasDesktopReqs)
	}
	wg.Wait()

	s.mu.Lock()
	if hasSettings {
		s.snapshot.Settings = settings
	}
	if hasAppearance {
		s.snapshot.Appearance = appearance
	}
	if hasProxy {
		s.snapshot.Proxy = proxy
		s.snapshot.LastError = ""
	} else if proxyErr != nil {
		s.snapshot.LastError = "读取代理状态失败：" + proxyErr.Error()
	}
	if hasResources {
		s.snapshot.Resources = resources
	}
	if hasBuild {
		s.snapshot.Build = build
	}
	if hasDiag {
		s.snapshot.Diagnostics = diag
	}
	if hasMobile {
		s.snapshot.MobilePair = mobile
	}
	if hasFavs {
		s.snapshot.Favorites = favs
	}
	if hasTerms {
		s.snapshot.Terminals = terminalsResp
	}
	if hasCLI {
		s.snapshot.CLISessions = cliResp.Sessions
	}
	if hasClaude {
		s.snapshot.ClaudePanel = claudePanel
	}
	if hasClaudeDesktop {
		s.snapshot.ClaudeDesktop = claudeDesktop
	}
	if hasTabs {
		s.snapshot.BrowserTabs = browserTabs
	}
	if hasTailnet {
		s.snapshot.Tailscale = tailnet
	}
	if hasDesktopReqs {
		s.snapshot.DesktopFileRequests = desktopReqs.Requests
	}
	s.mu.Unlock()

	if ready && vmRunning {
		s.ensureBrowserSSE(ctx)
	} else {
		s.stopBrowserSSE()
	}
}

func (s *Store) ensureBrowserSSE(parentCtx context.Context) {
	s.browserSSEMu.Lock()
	if s.sseCancel != nil {
		s.browserSSEMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parentCtx)
	s.sseCancel = cancel
	s.browserSSEMu.Unlock()

	go func() {
		defer func() {
			s.browserSSEMu.Lock()
			if s.sseCancel != nil {
				s.sseCancel = nil
			}
			s.browserSSEMu.Unlock()
		}()
		for {
			if ctx.Err() != nil {
				return
			}
			resp, err := s.client.OpenSSE(ctx, "/api/browser/events")
			if err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
					continue
				}
			}
			s.consumeSSE(ctx, resp)
		}
	}()
}

func (s *Store) stopBrowserSSE() {
	s.browserSSEMu.Lock()
	cancel := s.sseCancel
	s.sseCancel = nil
	s.browserSSEMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Store) consumeSSE(ctx context.Context, resp *http.Response) {
	defer resp.Body.Close()
	reader := bufio.NewReaderSize(resp.Body, 32<<10)
	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		raw := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		var tabs BrowserTabsView
		if json.Unmarshal([]byte(raw), &tabs) == nil && (tabs.Tabs != nil || tabs.Uploads != nil || tabs.Downloads != nil || tabs.Ready) {
			s.mu.Lock()
			s.snapshot.BrowserTabs = tabs
			s.mu.Unlock()
			s.notify()
			return
		}
		s.TriggerRefresh()
	}

	for {
		if ctx.Err() != nil {
			return
		}
		line, err := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			flush()
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if err != nil {
			if err == io.EOF {
				flush()
			}
			return
		}
	}
}

// UpsertTerminal inserts or updates a terminal summary in the snapshot so UI
// state can switch to a newly created terminal before the next full poll.
func (s *Store) UpsertTerminal(term TerminalSummary) {
	if term.ID == "" {
		return
	}
	s.mu.Lock()
	replaced := false
	for i := range s.snapshot.Terminals {
		if s.snapshot.Terminals[i].ID == term.ID {
			s.snapshot.Terminals[i] = term
			replaced = true
			break
		}
	}
	if !replaced {
		s.snapshot.Terminals = append(s.snapshot.Terminals, term)
	}
	s.mu.Unlock()
	s.notify()
}

// RemoveTerminal removes a terminal summary from the snapshot immediately
// after closing it, prior to the next background refresh.
func (s *Store) RemoveTerminal(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	filtered := s.snapshot.Terminals[:0]
	changed := false
	for _, t := range s.snapshot.Terminals {
		if t.ID == id {
			changed = true
			continue
		}
		filtered = append(filtered, t)
	}
	if changed {
		s.snapshot.Terminals = append([]TerminalSummary(nil), filtered...)
	}
	s.mu.Unlock()
	if changed {
		s.notify()
	}
}

func (s *Store) RefreshVault(ctx context.Context) error {
	var vault VaultData
	if err := s.client.GetJSON(ctx, "/api/vault", &vault); err != nil {
		return err
	}
	s.mu.Lock()
	s.snapshot.Vault = vault
	s.mu.Unlock()
	s.notify()
	return nil
}

func (s *Store) RefreshSavedDownloads(ctx context.Context) error {
	var out struct {
		Downloads []BrowserDownload `json:"downloads"`
	}
	if err := s.client.GetJSON(ctx, "/api/browser/files/saved", &out); err != nil {
		return err
	}
	s.mu.Lock()
	s.snapshot.SavedDownloads = out.Downloads
	s.mu.Unlock()
	s.notify()
	return nil
}

func (s *Store) CheckUpdatesAbout(ctx context.Context) error {
	var about AboutCheck
	if err := s.client.PostJSON(ctx, "/api/client/about", map[string]any{}, &about); err != nil {
		return err
	}
	s.mu.Lock()
	s.snapshot.About = about
	s.mu.Unlock()
	s.notify()
	return nil
}

// RunTrackedJob posts an asynchronous workbench mutation (such as /api/proxy/* or
// /api/safety/check), streams live job status from /api/status into the Store until
// completion, and refreshes all snapshots before returning.
func (s *Store) RunTrackedJob(ctx context.Context, path string, payload any) (Job, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
	}
	var accepted Job
	if err := s.client.PostJSON(ctx, path, payload, &accepted); err != nil {
		return accepted, err
	}
	if accepted.ID == "" {
		s.RefreshAll(ctx)
		return accepted, nil
	}

	s.mu.Lock()
	s.snapshot.Status.Job = accepted
	s.mu.Unlock()
	s.notify()

	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	for {
		var st StatusView
		if err := s.client.GetJSON(ctx, "/api/status", &st); err == nil {
			s.mu.Lock()
			s.snapshot.Status = st
			s.mu.Unlock()
			s.notify()

			if st.Job.ID != "" && st.Job.ID != accepted.ID {
				s.RefreshAll(ctx)
				return st.Job, errors.New("操作已被其他任务替代，请查看当前状态")
			}
			if !st.Job.Running {
				s.RefreshAll(ctx)
				if st.Job.Error != "" {
					return st.Job, errors.New(st.Job.Error)
				}
				if st.Job.Canceled {
					return st.Job, errors.New("操作已取消")
				}
				return st.Job, nil
			}
		}

		select {
		case <-ctx.Done():
			return accepted, ctx.Err()
		case <-ticker.C:
		}
	}
}
