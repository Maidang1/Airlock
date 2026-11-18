package herdrbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu      sync.Mutex
	health  HealthInfo
	panes   map[string]*fakePane
	runs    []RunOptions
	sends   [][2]string
	waitFn  func(pane string, timeout time.Duration, p *fakePane) (WaitResult, error)
	kills   []string
	removes []string
}

type fakePane struct {
	info   PaneInfo
	output string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		health: HealthInfo{Available: true, Backend: "hc", HCPath: "/usr/local/bin/hc"},
		panes:  make(map[string]*fakePane),
	}
}

func (f *fakeRunner) Health(context.Context) HealthInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.health
}

func (f *fakeRunner) RunBackground(_ context.Context, opts RunOptions) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, opts)
	f.panes[opts.PaneName] = &fakePane{
		info: PaneInfo{
			ID:     opts.PaneName,
			Name:   opts.PaneName,
			Status: "idle",
		},
		output: "started " + opts.Command,
	}
	return opts.PaneName, opts.HerdrKind != "" && opts.Interactive, nil
}

func (f *fakeRunner) Send(_ context.Context, ref PaneRef, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.panes[ref.Name]
	if !ok {
		return errors.New("pane not found")
	}
	f.sends = append(f.sends, [2]string{ref.Name, text})
	p.output += "\n> " + text + "\nreply to: " + text
	return nil
}

func (f *fakeRunner) PromptAndWait(ctx context.Context, ref PaneRef, text string, timeout time.Duration) (WaitResult, error) {
	if err := f.Send(ctx, ref, text); err != nil {
		return WaitResult{}, err
	}
	return f.Wait(ctx, ref, timeout)
}

func (f *fakeRunner) Wait(_ context.Context, ref PaneRef, timeout time.Duration) (WaitResult, error) {
	f.mu.Lock()
	p, ok := f.panes[ref.Name]
	fn := f.waitFn
	f.mu.Unlock()
	if !ok {
		return WaitResult{}, errors.New("pane not found")
	}
	if fn != nil {
		return fn(ref.Name, timeout, p)
	}
	return WaitResult{Completed: true}, nil
}

func (f *fakeRunner) Read(_ context.Context, ref PaneRef, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.panes[ref.Name]
	if !ok {
		return "", errors.New("pane not found")
	}
	return StripANSI(p.output), nil
}

func (f *fakeRunner) List(context.Context) ([]PaneInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]PaneInfo, 0, len(f.panes))
	for _, p := range f.panes {
		out = append(out, p.info)
	}
	return out, nil
}

func (f *fakeRunner) Kill(_ context.Context, ref PaneRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills = append(f.kills, ref.Name)
	if p, ok := f.panes[ref.Name]; ok {
		exited := true
		p.info.Exited = &exited
		p.info.Status = "exited"
	}
	return nil
}

func (f *fakeRunner) Remove(_ context.Context, ref PaneRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes = append(f.removes, ref.Name)
	delete(f.panes, ref.Name)
	return nil
}

func TestLoadConfigMergesDefaultsAndCustomProfiles(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig missing file: %v", err)
	}
	if _, ok := cfg.Profiles["codex"]; !ok {
		t.Fatal("expected built-in codex profile")
	}
	if _, ok := cfg.Profiles["claude"]; !ok {
		t.Fatal("expected built-in claude profile")
	}

	custom := FileConfig{
		HerdrPath:   "/opt/homebrew/bin/herdr",
		SocketPath:  "/tmp/herdr.sock",
		Session:     "work",
		MaxSessions: 4,
		Profiles: []AgentProfile{
			{Name: "opencode", Disabled: true},
			{
				Name:        "gemini",
				Description: "Gemini CLI",
				Command:     "/opt/homebrew/bin/gemini",
				HerdrKind:   "gemini",
				DefaultMode: "default",
				Modes: map[string]AgentMode{
					"default": {Args: []string{"--sandbox"}, Interactive: true, ReadOnly: false},
				},
			},
		},
	}
	data, _ := json.Marshal(custom)
	if err := os.WriteFile(filepath.Join(dir, DefaultConfigFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig custom: %v", err)
	}
	if cfg.HerdrPath != "/opt/homebrew/bin/herdr" || cfg.SocketPath != "/tmp/herdr.sock" || cfg.Session != "work" || cfg.MaxSessions != 4 {
		t.Fatalf("unexpected scalar settings: %+v", cfg)
	}
	if _, ok := cfg.Profiles["opencode"]; ok {
		t.Fatal("expected disabled opencode profile to be omitted")
	}
	if _, ok := cfg.Profiles["gemini"]; !ok {
		t.Fatal("expected custom gemini profile")
	}
}

func TestStripANSIAndOutputDelta(t *testing.T) {
	raw := "\x1b]0;codex\x07\x1b[32mhello\x1b[0m\r\nprogress 10%\rprogress 100%\r\nline 2\n"
	got := StripANSI(raw)
	want := "hello\nprogress 100%\nline 2"
	if got != want {
		t.Fatalf("StripANSI:\n got  %q\n want %q", got, want)
	}
	prev := "line 1\nline 2"
	curr := "line 2\nline 3\nline 4"
	if delta := OutputDelta(prev, curr); delta != "line 3\nline 4" {
		t.Fatalf("OutputDelta = %q", delta)
	}
}

func TestHerdrCLIRunnerParsers(t *testing.T) {
	rawStatus := `client:
  version: 0.9.5

server:
  status: running
  version: 0.9.5
  socket: /Users/test/.config/herdr/herdr.sock
`
	st := parseHerdrStatus(rawStatus)
	if st.ClientVersion != "0.9.5" || st.ServerStatus != "running" || st.ServerVersion != "0.9.5" || st.SocketPath != "/Users/test/.config/herdr/herdr.sock" {
		t.Fatalf("parseHerdrStatus: %+v", st)
	}

	code, msg, ok := parseHerdrError([]byte(`{"error":{"code":"agent_blocked","message":"waiting for confirmation"}}`), nil)
	if !ok || code != "agent_blocked" || msg == "" {
		t.Fatalf("parseHerdrError: ok=%v code=%q msg=%q", ok, code, msg)
	}

	readText := parseHerdrReadText([]byte(`{"id":"1","result":{"read":{"text":"\u001b[32mready\u001b[0m\n"},"type":"pane_read"}}`))
	if readText != "ready" {
		t.Fatalf("parseHerdrReadText = %q", readText)
	}

	waitBlocked := parseHerdrAgentWaitOutput([]byte(`{"result":{"agent":{"agent_status":"blocked"}}}`))
	if !waitBlocked.Blocked {
		t.Fatalf("expected Blocked=true for blocked status: %+v", waitBlocked)
	}

	// Herdr 0.9+ `done` means ready for next prompt, not exited.
	donePane := PaneInfo{PaneID: "w1:p1", Name: "cage-codex-1234", AgentStatus: "done"}
	if isPaneExited(donePane) {
		t.Fatal("agent_status=done must not be treated as exited")
	}
	status, _ := classifyPaneStatus(donePane, true, WaitResult{}, "")
	if status != SessionStatusIdle {
		t.Fatalf("expected idle for agent_status=done, got %q", status)
	}
}

func TestInstallHostValidationAndDetection(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHost(context.Background(), cfg, "bogus", false, nil); err == nil {
		t.Fatal("expected unsupported install method to fail")
	}
	// Missing configured binary produces clean not-installed status without error panic.
	cfg.HerdrPath = filepath.Join(t.TempDir(), "missing-herdr")
	cfg.HCPath = filepath.Join(t.TempDir(), "missing-hc")
	st := DetectHost(context.Background(), cfg)
	if st.ServerRunning {
		t.Fatalf("expected ServerRunning=false for missing binaries: %+v", st)
	}
}

func TestInteractiveSessionLifecycleAndGuards(t *testing.T) {
	repo := t.TempDir()
	other := t.TempDir()
	runner := newFakeRunner()
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(cfg, runner, func(req string) (string, error) {
		if req == "/guest/repo" || req == repo {
			return repo, nil
		}
		return "", errors.New("workdir is outside authorized projects")
	})
	mgr.startupSettle = 0

	ctx := context.Background()
	if _, err := mgr.StartSession(ctx, StartRequest{Agent: "codex", Workdir: other}); err == nil {
		t.Fatal("expected unauthorized workdir to fail")
	}
	if _, err := mgr.StartSession(ctx, StartRequest{Agent: "unknown-bin", Workdir: repo}); err == nil {
		t.Fatal("expected non-whitelisted agent to fail")
	}

	// Start interactive write session on repo.
	sess, err := mgr.StartSession(ctx, StartRequest{
		Agent:         "codex",
		Mode:          "workspace-write",
		Workdir:       "/guest/repo",
		InitialPrompt: "inspect $(touch /tmp/pwned) && fix bug",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sess.Status != SessionStatusIdle || !sess.Interactive || sess.ReadOnly {
		t.Fatalf("unexpected session view: %+v", sess)
	}
	if len(runner.runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runner.runs))
	}
	gotRun := runner.runs[0]
	wantArgs := []string{"--sandbox", "workspace-write", "-C", repo}
	if gotRun.Command != "codex" || !slices.Equal(gotRun.Args, wantArgs) || gotRun.Workdir != repo {
		t.Fatalf("unexpected run options: %+v", gotRun)
	}

	// Second concurrent write session on overlapping workdir must be rejected.
	if _, err := mgr.StartSession(ctx, StartRequest{
		Agent:   "claude",
		Mode:    "accept-edits",
		Workdir: repo,
	}); err == nil {
		t.Fatal("expected concurrent write session on same workdir to be rejected")
	}

	// Read-only session on same workdir IS allowed concurrently.
	ro, err := mgr.StartSession(ctx, StartRequest{
		Agent:   "claude",
		Mode:    "plan",
		Workdir: repo,
	})
	if err != nil {
		t.Fatalf("expected concurrent read-only session to succeed: %v", err)
	}

	// Multi-turn send + incremental delta + timeout/waiting_approval handling.
	turn, err := mgr.SendMessage(ctx, SendRequest{
		SessionID: sess.SessionID,
		Message:   "now add a unit test",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !strings.Contains(turn.NewOutput, "reply to: now add a unit test") || strings.Contains(turn.NewOutput, "started codex") {
		t.Fatalf("expected incremental delta only, got %q", turn.NewOutput)
	}

	// Simulate waiting_approval state detected from pane/agent list.
	runner.mu.Lock()
	blocked := true
	runner.panes[sess.PaneName].info.Blocked = &blocked
	runner.panes[sess.PaneName].info.Status = "blocked"
	runner.mu.Unlock()
	readView, err := mgr.ReadSession(ctx, ReadRequest{SessionID: sess.SessionID})
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if readView.Status != SessionStatusWaitingApproval {
		t.Fatalf("expected waiting_approval, got %q", readView.Status)
	}

	// Simulate wait timeout -> status working instead of error.
	runner.mu.Lock()
	runner.panes[sess.PaneName].info.Blocked = nil
	runner.panes[sess.PaneName].info.Status = "working"
	runner.waitFn = func(string, time.Duration, *fakePane) (WaitResult, error) {
		return WaitResult{TimedOut: true}, nil
	}
	runner.mu.Unlock()
	workingView, err := mgr.SendMessage(ctx, SendRequest{
		SessionID:      sess.SessionID,
		Message:        "keep working",
		WaitTimeoutSec: 1,
	})
	if err != nil {
		t.Fatalf("SendMessage on timeout should not error: %v", err)
	}
	if workingView.Status != SessionStatusWorking {
		t.Fatalf("expected working status on timeout, got %q", workingView.Status)
	}

	// Unowned session ID must fail.
	if _, err := mgr.ReadSession(ctx, ReadRequest{SessionID: "other-pane"}); err == nil {
		t.Fatal("expected unowned session_id to fail")
	}

	// Close sessions cleanly.
	closedView, err := mgr.CloseSession(ctx, sess.SessionID)
	if err != nil || closedView.Status != SessionStatusClosed || !closedView.Closed {
		t.Fatalf("CloseSession: %+v err=%v", closedView, err)
	}
	if _, err := mgr.CloseSession(ctx, ro.SessionID); err != nil {
		t.Fatalf("CloseSession ro: %v", err)
	}
}

func TestMCPServerAuthAndTools(t *testing.T) {
	repo := t.TempDir()
	runner := newFakeRunner()
	cfg, _ := LoadConfig("")
	mgr := NewManager(cfg, runner, func(req string) (string, error) {
		if req == repo {
			return repo, nil
		}
		return "", errors.New("denied")
	})
	mgr.startupSettle = 0

	srv := NewServer(mgr, "secret-token")

	// Unauthenticated request rejected.
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer token, got %d", rec.Code)
	}

	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer secret-token")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}

	initResp := call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if initResp.Code != http.StatusOK || !strings.Contains(initResp.Body.String(), "cage-herdr-bridge") {
		t.Fatalf("initialize failed: %d %s", initResp.Code, initResp.Body.String())
	}

	notifResp := call(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if notifResp.Code != http.StatusAccepted {
		t.Fatalf("notification expected 202, got %d", notifResp.Code)
	}

	listResp := call(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if listResp.Code != http.StatusOK {
		t.Fatalf("tools/list: %d", listResp.Code)
	}
	for _, tool := range []string{"list_host_agents", "start_agent_session", "send_agent_message", "read_agent_session", "close_agent_session"} {
		if !strings.Contains(listResp.Body.String(), tool) {
			t.Fatalf("missing tool %q in %s", tool, listResp.Body.String())
		}
	}

	startPayload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "start_agent_session",
			"arguments": map[string]any{
				"agent":          "codex",
				"mode":           "read-only",
				"workdir":        repo,
				"initial_prompt": "plan refactor",
			},
		},
	})
	startResp := call(string(startPayload))
	if startResp.Code != http.StatusOK || !strings.Contains(startResp.Body.String(), `"isError":false`) {
		t.Fatalf("start_agent_session failed: %d %s", startResp.Code, startResp.Body.String())
	}

	badPayload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "start_agent_session",
			"arguments": map[string]any{
				"agent":   "codex",
				"workdir": "/etc",
			},
		},
	})
	badResp := call(string(badPayload))
	if badResp.Code != http.StatusOK || !strings.Contains(badResp.Body.String(), `"isError":true`) {
		t.Fatalf("expected tool isError:true for denied workdir, got %d %s", badResp.Code, badResp.Body.String())
	}
}
