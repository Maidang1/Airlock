package views

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
	"github.com/sylvinus/agent-vm/internal/mygoclient/surfaces"
)

type testHostBridge struct{}

func (testHostBridge) OpenDialog(core.NativeDialogOptions) ([]string, error) { return nil, nil }
func (testHostBridge) SaveDialog(core.NativeDialogOptions) (string, error)   { return "", nil }
func (testHostBridge) OpenExternal(string) error                             { return nil }
func (testHostBridge) OpenPath(string) error                                 { return nil }
func (testHostBridge) ShowItemInFolder(string) error                         { return nil }
func (testHostBridge) ShowAndFocusWindow()                                   {}
func (testHostBridge) MarkWorkbenchReady()                                   {}
func (testHostBridge) HandleUpdate(op string) map[string]any {
	return map[string]any{"phase": "idle", "currentVersion": "1.0.0-test", "op": op}
}

type mockWorkbenchState struct {
	mu                  sync.Mutex
	accountExists       bool
	unlocked            bool
	initRequired        bool
	macCredsUnready     bool
	desktopGateway      bool
	trustedLogin        bool
	trustedLocal        bool
	trustedLoginActions []string
	proxySelected       string
	proxyPort           int
	safetyChecks        int
	calledPaths         []string
}

func newMockWorkbench(t *testing.T, initial *mockWorkbenchState) (*httptest.Server, *RootView, *ui.Tester) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		initial.mu.Lock()
		initial.calledPaths = append(initial.calledPaths, r.Method+" "+r.URL.Path)
		acctExists := initial.accountExists
		unlocked := initial.unlocked
		initReq := initial.initRequired
		macCredsUnready := initial.macCredsUnready
		desktopGateway := initial.desktopGateway
		trustedLogin := initial.trustedLogin
		trustedLocal := initial.trustedLocal
		selNode := initial.proxySelected
		proxyPort := initial.proxyPort
		if proxyPort == 0 {
			proxyPort = 7890
		}
		initial.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/session":
			http.SetCookie(w, &http.Cookie{Name: "cage_session_test", Value: "tok", Path: "/"})
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/launch":
			_ = json.NewEncoder(w).Encode(map[string]string{"url": srv.URL + "/#t2"})
		case "/api/agent/native/window":
			_, _ = w.Write([]byte(`{"window_proof":"wp1"}`))
		case "/api/client/context":
			_ = json.NewEncoder(w).Encode(core.ClientContext{
				DesktopGateway: desktopGateway, Mode: "local", ConnectionID: "local", ClientPlatform: "darwin", ServerPlatform: "darwin",
			})
		case "/api/client/trusted-login":
			var in struct {
				Action string `json:"action"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			initial.mu.Lock()
			initial.trustedLoginActions = append(initial.trustedLoginActions, in.Action)
			switch in.Action {
			case "enable":
				initial.trustedLogin = true
			case "disable":
				initial.trustedLogin = false
			case "resume":
				initial.unlocked = true
			}
			initial.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/client/local-status":
			_ = json.NewEncoder(w).Encode(core.DesktopLocalStatus{
				Available: true, Known: true, ServiceRunning: true, ControllerID: "ctrl-1",
			})
		case "/api/client/hosts":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hosts": []core.HostProfile{
					{ID: "h1", Name: "Cloud Linux", Host: "10.0.0.5", Port: 22, User: "ubuntu", Platform: "linux", Auth: core.HostAuthConfig{Kind: "agent"}},
				},
			})
		case "/api/account":
			_ = json.NewEncoder(w).Encode(core.AccountView{
				Exists:                acctExists,
				Unlocked:              unlocked,
				Username:              "alice",
				RecoveryReady:         true,
				MacOSAccountHost:      true,
				MacOSCredentialsReady: unlocked && !macCredsUnready,
				TrustedLogin:          trustedLogin,
				TrustedLocal:          trustedLocal,
				DeviceBinding:         "bind-1",
			})
		case "/api/account/prepare-macos":
			initial.mu.Lock()
			isUnlocked := initial.unlocked
			if isUnlocked {
				initial.macCredsUnready = false
			}
			initial.mu.Unlock()
			if !isUnlocked {
				http.Error(w, "请先解锁 Cage", http.StatusLocked)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/account/create":
			initial.mu.Lock()
			initial.accountExists = true
			initial.unlocked = true
			initial.mu.Unlock()
			_ = json.NewEncoder(w).Encode(core.AccountView{
				Exists: true, Unlocked: true, Username: "alice", RecoveryKey: "RK-TEST-1234-ABCD",
			})
		case "/api/account/unlock":
			initial.mu.Lock()
			initial.unlocked = true
			initial.mu.Unlock()
			_ = json.NewEncoder(w).Encode(core.AccountView{
				Exists: true, Unlocked: true, Username: "alice",
			})
		case "/api/environment/initialization":
			if r.Method == http.MethodPost {
				var in struct {
					Action string `json:"action"`
				}
				_ = json.NewDecoder(r.Body).Decode(&in)
				if in.Action == "complete" {
					initial.mu.Lock()
					initial.initRequired = false
					initial.mu.Unlock()
				}
				_, _ = w.Write([]byte(`{"completed":true}`))
				return
			}
			_ = json.NewEncoder(w).Encode(core.InitializationView{
				Required: initReq, Prepared: true, Tracked: true,
			})
		case "/api/environment/guest-systems":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"system": "macos", "available": true, "linux_systems": []string{"debian-13"},
			})
		case "/api/status":
			_ = json.NewEncoder(w).Encode(core.StatusView{
				Ready:          true,
				VMRunning:      false,
				Workspace:      "/Users/alice/projects/demo",
				Projects:       []string{"/Users/alice/projects/demo"},
				LocalVMStorage: true,
				GuestSystem:    "macos",
				Safety: core.SafetyState{
					ExitIP: "203.0.113.10",
					Gate: &core.GateStatus{
						Mode: "open",
						Report: &core.SafetyReport{
							Policy: "managed-proxy",
							Checks: []core.SafetyCheck{{ID: "egress", Title: "受管代理出口", State: "pass", Evidence: "203.0.113.10"}},
						},
					},
					Report: &core.SafetyReport{
						Policy: "managed-proxy",
						Checks: []core.SafetyCheck{{ID: "egress", Title: "受管代理出口", State: "pass", Evidence: "203.0.113.10"}},
					},
				},
			})
		case "/api/settings":
			_ = json.NewEncoder(w).Encode(core.SettingsView{
				Keyboard: "mac", BrowserHomeURL: "https://www.bing.com/", ClaudeTerminalCommand: "claude",
			})
		case "/api/appearance":
			_ = json.NewEncoder(w).Encode(core.Appearance{Mode: "system", CodeFontSize: 13})
		case "/api/proxy":
			_ = json.NewEncoder(w).Encode(core.ProxyStatusView{
				Selected:      selNode,
				Port:          proxyPort,
				State:         "prepared",
				StateLabel:    "待启动环境",
				Revision:      "rev-1",
				CoreAvailable: true,
				BootstrapDNS:  "https://1.1.1.1/dns-query",
				Route:         []string{selNode},
				Sources: []core.ProxySource{
					{
						ID:        "src-1",
						Name:      "主订阅",
						Remote:    true,
						Host:      "sub.example.com",
						UpdatedAt: time.Now(),
						Usage:     "upload=1024; download=2048; total=10485760",
						Nodes: []core.ProxyNode{
							{ID: "node-a", Name: "Tokyo 01", Type: "trojan"},
							{ID: "node-b", Name: "Singapore 02", Type: "vless"},
						},
						Skipped: []core.ProxyNodeIssue{
							{Index: 3, Name: "Unsupported", Type: "ss", Reason: "unsupported cipher"},
						},
					},
				},
			})
		case "/api/proxy/port", "/api/proxy/restart":
			var in struct {
				Port int `json:"port"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Port >= 1024 && in.Port <= 65535 {
				initial.mu.Lock()
				initial.proxyPort = in.Port
				initial.mu.Unlock()
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/proxy/select":
			var in struct {
				NodeID string `json:"node_id"`
				Port   int    `json:"port"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			initial.mu.Lock()
			initial.proxySelected = in.NodeID
			if in.Port >= 1024 && in.Port <= 65535 {
				initial.proxyPort = in.Port
			}
			initial.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/safety/check":
			initial.mu.Lock()
			initial.safetyChecks++
			initial.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/environment/resources":
			_ = json.NewEncoder(w).Encode(core.ResourceStatus{CPUs: 4, MemoryGiB: 8, MaxCPUs: 10, MaxMemoryGiB: 32})
		case "/api/client/about":
			_ = json.NewEncoder(w).Encode(core.BuildInfo{Version: "1.2.3", Commit: "abc123"})
		case "/api/diagnostics":
			_ = json.NewEncoder(w).Encode(core.DiagnosticsInfo{Files: 2, Bytes: 2048})
		case "/api/mobile-pair":
			_ = json.NewEncoder(w).Encode(core.MobilePairStatus{Paired: false, Status: "未配对"})
		case "/api/favorites":
			_ = json.NewEncoder(w).Encode(core.Favorites{
				Items: []core.Favorite{{ID: "f1", Title: "Anthropic", URL: "https://anthropic.com"}},
			})
		case "/api/terminals":
			_ = json.NewEncoder(w).Encode(map[string]any{"terminals": []core.TerminalSummary{}})
		case "/api/cli/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []core.CLISessionSummary{}})
		case "/api/claude-accounts":
			_ = json.NewEncoder(w).Encode(core.ClaudePanelView{
				Scheduling: core.ClaudeScheduling{Mode: "balanced", SwitchPolicy: "ask"},
				Accounts: []core.ClaudeAccount{
					{
						ID: "ca1", Label: "工作主号", Email: "alice@example.com", Enabled: true, Status: "ready",
						Quota: &core.ClaudeQuota{FiveHour: &core.ClaudeWindow{UsedPercentage: 25}},
					},
				},
			})
		case "/api/tailscale":
			_ = json.NewEncoder(w).Encode(core.TailnetStatus{Installed: true, Connected: false})
		case "/api/claude-desktop":
			_ = json.NewEncoder(w).Encode(core.ClaudeDesktopState{Installed: true, Running: false})
		case "/api/browser/tabs":
			_ = json.NewEncoder(w).Encode(core.BrowserTabsView{Ready: true, Available: true})
		case "/api/input/files/desktop/requests":
			_ = json.NewEncoder(w).Encode(map[string]any{"requests": []core.DesktopFileRequest{}})
		case "/api/vault":
			_ = json.NewEncoder(w).Encode(core.VaultData{
				Entries: []core.VaultEntry{{ID: "v1", Origin: "https://github.com", Username: "alice"}},
			})
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(srv.Close)

	client := core.NewControllerClient(core.ClientOptions{
		ConfigPath: filepath.Join(t.TempDir(), "config.json"),
		TestEntryFn: func(context.Context) (core.DesktopResult, error) {
			return core.DesktopResult{Status: "ready", URL: srv.URL + "/#t1"}, nil
		},
	})
	store := core.NewStore(client)
	audio := core.NewAudioStream(client)
	clip := core.NewClipboardSync(client)
	t.Cleanup(func() {
		audio.Stop()
		clip.Stop()
		store.Stop()
	})

	termMgr := surfaces.NewTerminalManager(client, func(string) {}, func() {})
	macB := surfaces.NewMacOSWindowSurface(client, true, func() {})
	macC := surfaces.NewMacOSWindowSurface(client, false, func() {})
	vncBridge := surfaces.NewVNCBridge(client, clip, func() {})
	vncMgr := surfaces.NewVNCSurfaceManager(client, func() {})

	rv := NewRootView(store, testHostBridge{}, audio, clip, termMgr, macB, macC, vncMgr, vncBridge, func() {})
	if err := store.BootstrapAndRefresh(context.Background()); err != nil {
		t.Fatalf("BootstrapAndRefresh: %v", err)
	}
	tester := ui.NewTester(rv.View, 1440, 900)
	return srv, rv, tester
}

func waitTester(t *testing.T, tester *ui.Tester, cond func() bool, desc string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tester.Frame()
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; visible texts=%v", desc, tester.Texts())
}

func TestAccountUnlockAndCreateFlowUI(t *testing.T) {
	// 1. Account creation -> Recovery Key confirmation -> Workbench
	stCreate := &mockWorkbenchState{accountExists: false, unlocked: false, initRequired: false, proxySelected: "node-a"}
	_, rv1, tester1 := newMockWorkbench(t, stCreate)
	tester1.Frame()
	if !tester1.HasText("创建本机 Cage 安全账户") {
		t.Fatalf("expected account create heading, got texts=%v", tester1.Texts())
	}
	if err := tester1.Click("用户名"); err != nil {
		t.Fatalf("focus username failed: %v", err)
	}
	tester1.Type("alice")
	if err := tester1.Click("密码"); err != nil {
		t.Fatalf("focus password failed: %v", err)
	}
	tester1.Type("pass1234")
	if err := tester1.Click("确认密码"); err != nil {
		t.Fatalf("focus confirm failed: %v", err)
	}
	tester1.Type("pass1234")
	if err := tester1.Click("创建账户"); err != nil {
		t.Fatalf("click create failed: %v", err)
	}
	waitTester(t, tester1, func() bool { return tester1.HasText("RK-TEST-1234-ABCD") }, "recovery key screen")

	if err := tester1.Click("我已安全保存该恢复密钥"); err != nil {
		t.Fatalf("check recovery confirmed failed: %v", err)
	}
	if err := tester1.Click("继续进入 Cage"); err != nil {
		t.Fatalf("click continue failed: %v", err)
	}
	waitTester(t, tester1, func() bool { return tester1.HasText("终端与 Claude") }, "workbench after create")
	_ = rv1

	// 2. Password unlock flow -> Workbench
	stUnlock := &mockWorkbenchState{accountExists: true, unlocked: false, initRequired: false, proxySelected: "node-a"}
	_, _, tester2 := newMockWorkbench(t, stUnlock)
	tester2.Frame()
	if !tester2.HasText("解锁 Cage · alice") {
		t.Fatalf("expected unlock screen, got texts=%v", tester2.Texts())
	}
	if err := tester2.Click("解锁密码"); err != nil {
		t.Fatalf("focus unlock pw: %v", err)
	}
	tester2.Type("pass1234")
	if err := tester2.Click("解锁"); err != nil {
		t.Fatalf("click unlock: %v", err)
	}
	waitTester(t, tester2, func() bool { return tester2.HasText("启动环境") }, "workbench sidebar after unlock")
}

func TestOnboardingCompletionFlowUI(t *testing.T) {
	st := &mockWorkbenchState{accountExists: true, unlocked: true, initRequired: true, proxySelected: "node-a"}
	_, _, tester := newMockWorkbench(t, st)
	tester.Frame()
	if !tester.HasText("初始化 Cage 隔离工作环境") {
		t.Fatalf("expected onboarding screen, got %v", tester.Texts())
	}
	if !tester.HasText("Tokyo 01") || !tester.HasText("Singapore 02") || !tester.HasText("设为出口") {
		t.Fatalf("expected imported proxy nodes in onboarding view, got %v", tester.Texts())
	}
	if err := tester.Click("完成初始化并进入工作台"); err != nil {
		t.Fatalf("click complete onboarding: %v", err)
	}
	waitTester(t, tester, func() bool { return tester.HasText("终端与 Claude Code") }, "workbench after onboarding")
}

func TestWorkbenchNavigationAndMutationsUI(t *testing.T) {
	st := &mockWorkbenchState{accountExists: true, unlocked: true, initRequired: false, proxySelected: "node-a"}
	_, rv, tester := newMockWorkbench(t, st)
	tester.Frame()

	// Audio mute button mutation in sidebar
	if err := tester.Click("静音"); err != nil {
		t.Fatalf("click mute: %v", err)
	}
	tester.Frame()
	if !rv.Store.Snapshot().ClientPrefs.AudioMuted || !tester.HasText("已静音") {
		t.Fatal("expected AudioMuted toggled via UI button")
	}

	// Verify all major workbench sections render cleanly
	checks := []struct {
		nav  string
		text string
	}{
		{NavTerminal, "暂无打开的终端"},
		{NavBrowser, "隔离浏览器尚未启动"},
		{NavClaudeDesktop, "Claude Desktop"},
		{NavProxy, "内置代理与节点路由（Mihomo）"},
		{NavSafety, "隔离网络与安全检查"},
		{NavClaudeAccounts, "工作主号"},
		{NavTailscale, "Tailscale 私网组网"},
		{NavRemoteHosts, "Cloud Linux"},
		{NavBrowserMgmt, "https://github.com"},
		{NavSettings, "设置、安全与维护"},
	}
	for _, tc := range checks {
		rv.SelectNav(tc.nav)
		waitTester(t, tester, func() bool { return tester.HasText(tc.text) }, tc.nav+" text "+tc.text)
	}

	// Interactive Proxy mutation: select second node "Singapore 02"
	rv.SelectNav(NavProxy)
	tester.Frame()
	if err := tester.Click("设为出口"); err != nil {
		t.Fatalf("click 设为出口: %v", err)
	}
	waitTester(t, tester, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.proxySelected == "node-b"
	}, "proxy node selection POST")
}

func TestMacOSCredentialsGateAndTrustedLoginUI(t *testing.T) {
	// 1. Locked existing account on a macOS host must show Unlock (not prepare-macos) and enable trusted login.
	stLocked := &mockWorkbenchState{
		accountExists:  true,
		unlocked:       false,
		initRequired:   false,
		desktopGateway: true,
		proxySelected:  "node-a",
	}
	_, _, tester1 := newMockWorkbench(t, stLocked)
	tester1.Frame()
	if !tester1.HasText("解锁 Cage · alice") {
		t.Fatalf("expected unlock heading on locked macOS host, got texts=%v", tester1.Texts())
	}
	if tester1.HasText("设置 macOS 客机共用账密") {
		t.Fatalf("locked account must not show prepare-macos gate before unlock, got texts=%v", tester1.Texts())
	}
	if err := tester1.Click("在此设备记住登录状态"); err != nil {
		t.Fatalf("check rememberLogin failed: %v", err)
	}
	if err := tester1.Click("解锁密码"); err != nil {
		t.Fatalf("focus unlock pw: %v", err)
	}
	tester1.Type("pass1234")
	if err := tester1.Click("解锁"); err != nil {
		t.Fatalf("click unlock: %v", err)
	}
	waitTester(t, tester1, func() bool { return tester1.HasText("启动环境") }, "workbench after unlock with desktop gateway")
	stLocked.mu.Lock()
	actions := append([]string(nil), stLocked.trustedLoginActions...)
	stLocked.mu.Unlock()
	if len(actions) != 1 || actions[0] != "enable" {
		t.Fatalf("expected trusted-login enable after unlock, got actions=%v", actions)
	}

	// 2. Unlocked account with initialization required + unready macOS creds shows prepare-macos first, then onboarding.
	stPrep := &mockWorkbenchState{
		accountExists:   true,
		unlocked:        true,
		initRequired:    true,
		macCredsUnready: true,
		proxySelected:   "node-a",
	}
	_, _, tester2 := newMockWorkbench(t, stPrep)
	tester2.Frame()
	if !tester2.HasText("设置 macOS 客机共用账密") {
		t.Fatalf("expected macOS credentials gate when unlocked + init required, got texts=%v", tester2.Texts())
	}
	if err := tester2.Click("设置密码"); err != nil {
		t.Fatalf("focus mac pw: %v", err)
	}
	tester2.Type("pass1234")
	if err := tester2.Click("确认密码"); err != nil {
		t.Fatalf("focus mac confirm: %v", err)
	}
	tester2.Type("pass1234")
	if err := tester2.Click("保存并准备环境"); err != nil {
		t.Fatalf("click prepare-macos: %v", err)
	}
	waitTester(t, tester2, func() bool { return tester2.HasText("初始化 Cage 隔离工作环境") }, "onboarding after prepare-macos")

	// 3. Locked account with remembered trusted_login auto-resumes on local desktop gateway.
	stResume := &mockWorkbenchState{
		accountExists:  true,
		unlocked:       false,
		initRequired:   false,
		desktopGateway: true,
		trustedLogin:   true,
		proxySelected:  "node-a",
	}
	_, _, tester3 := newMockWorkbench(t, stResume)
	waitTester(t, tester3, func() bool { return tester3.HasText("终端与 Claude") }, "auto-resume trusted login to workbench")
	stResume.mu.Lock()
	resumeActions := append([]string(nil), stResume.trustedLoginActions...)
	stResume.mu.Unlock()
	if len(resumeActions) != 1 || resumeActions[0] != "resume" {
		t.Fatalf("expected trusted-login resume call, got %v", resumeActions)
	}
}

func TestPortConflictDetectionAndCustomPortFlowUI(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen occupied port: %v", err)
	}
	defer ln.Close()
	occupiedPort := ln.Addr().(*net.TCPAddr).Port

	stConflict := &mockWorkbenchState{
		accountExists: true,
		unlocked:      true,
		initRequired:  true,
		proxyPort:     occupiedPort,
		proxySelected: "",
	}
	_, rv, tester := newMockWorkbench(t, stConflict)
	waitTester(t, tester, func() bool {
		return tester.HasText(fmt.Sprintf("端口 %d 冲突", occupiedPort))
	}, "port conflict badge on onboarding")

	if err := tester.Click("自动分配空闲端口"); err != nil {
		t.Fatalf("click auto-assign port: %v", err)
	}
	waitTester(t, tester, func() bool {
		stConflict.mu.Lock()
		p := stConflict.proxyPort
		stConflict.mu.Unlock()
		return p != occupiedPort && p >= 1024 && tester.HasText(fmt.Sprintf("端口 %d 空闲可用", p))
	}, "free proxy port assigned and applied")

	if err := tester.Click("设为出口"); err != nil {
		t.Fatalf("click 设为出口: %v", err)
	}
	waitTester(t, tester, func() bool {
		stConflict.mu.Lock()
		defer stConflict.mu.Unlock()
		return stConflict.proxySelected == "node-a" && stConflict.proxyPort != occupiedPort
	}, "select node using conflict-free port")

	// Also verify the dedicated Settings -> 端口与冲突检测 tab.
	stConflict.mu.Lock()
	stConflict.initRequired = false
	stConflict.mu.Unlock()
	rv.Store.RefreshAll(context.Background())
	rv.SelectNav(NavSettings)
	waitTester(t, tester, func() bool { return tester.HasText("端口与冲突检测") }, "settings tabs")
	if err := tester.Click("端口与冲突检测"); err != nil {
		t.Fatalf("click port settings tab: %v", err)
	}
	waitTester(t, tester, func() bool {
		return tester.HasText("端口配置与宿主机冲突检测") && tester.HasText("Cage 本机回环服务端口")
	}, "settings port conflict tab content")
}
