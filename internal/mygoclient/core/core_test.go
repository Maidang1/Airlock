package core

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

type fakeHostBridge struct {
	openPaths    []string
	savePath     string
	externalURLs []string
	focused      int
	readyCount   int
}

func (f *fakeHostBridge) OpenDialog(opts NativeDialogOptions) ([]string, error) {
	return f.openPaths, nil
}
func (f *fakeHostBridge) SaveDialog(opts NativeDialogOptions) (string, error) {
	return f.savePath, nil
}
func (f *fakeHostBridge) OpenExternal(u string) error {
	f.externalURLs = append(f.externalURLs, u)
	return nil
}
func (f *fakeHostBridge) OpenPath(string) error         { return nil }
func (f *fakeHostBridge) ShowItemInFolder(string) error { return nil }
func (f *fakeHostBridge) ShowAndFocusWindow()           { f.focused++ }
func (f *fakeHostBridge) MarkWorkbenchReady()           { f.readyCount++ }
func (f *fakeHostBridge) HandleUpdate(op string) map[string]any {
	return map[string]any{"phase": "idle", "op": op}
}

func TestControllerBootstrapSessionReauthLockAndConnectionScope(t *testing.T) {
	var (
		mu             sync.Mutex
		sessionCounter int
		ticketCounter  int
		seenOrigins    = map[string]string{}
		seenConn       = map[string]string{}
		forceUnauth    atomic.Bool
		forceLocked    atomic.Bool
	)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenOrigins[r.URL.Path] = r.Header.Get("Origin")
		seenConn[r.URL.Path] = r.Header.Get(connectionHeader)
		mu.Unlock()

		switch r.URL.Path {
		case "/api/session":
			if r.Header.Get("Origin") != srv.URL {
				http.Error(w, "bad origin", http.StatusForbidden)
				return
			}
			var in struct {
				Token string `json:"token"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Token == "" {
				http.Error(w, "bad token", http.StatusForbidden)
				return
			}
			mu.Lock()
			sessionCounter++
			val := "sess-" + string(rune('0'+sessionCounter))
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "cage_session_test", Value: val, Path: "/"})
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/launch":
			mu.Lock()
			ticketCounter++
			tk := "launch-" + string(rune('a'+ticketCounter))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"url": srv.URL + "/?launch=1#" + tk,
			})
		case "/api/agent/native/window":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"window_proof":"proof-xyz-123"}`))
		case "/api/client/context":
			_ = json.NewEncoder(w).Encode(ClientContext{Mode: "local", ConnectionID: "local"})
		case "/api/status":
			ck, err := r.Cookie("cage_session_test")
			if err != nil || ck.Value == "" {
				http.Error(w, "unauth", http.StatusUnauthorized)
				return
			}
			if forceUnauth.Swap(false) {
				http.Error(w, "expired", http.StatusUnauthorized)
				return
			}
			if forceLocked.Swap(false) {
				http.Error(w, "locked", http.StatusLocked)
				return
			}
			_ = json.NewEncoder(w).Encode(StatusView{Ready: true, VMRunning: true, Workspace: "/proj"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.json")
	client := NewControllerClient(ClientOptions{
		ConfigPath: cfgPath,
		TestEntryFn: func(ctx context.Context) (DesktopResult, error) {
			mu.Lock()
			ticketCounter++
			tk := "t-" + string(rune('a'+ticketCounter))
			mu.Unlock()
			return DesktopResult{
				Status: "ready",
				URL:    srv.URL + "/?launch=abc#" + tk,
			}, nil
		},
	})

	var lockedCount atomic.Int32
	client.SetOnLocked(func() { lockedCount.Add(1) })
	client.SetConnectionID("conn-remote-1")

	ctx := context.Background()
	if err := client.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap failed: %v", err)
	}
	if client.WindowProof() != "proof-xyz-123" {
		t.Fatalf("unexpected window proof: %q", client.WindowProof())
	}

	var st StatusView
	if err := client.GetJSON(ctx, "/api/status", &st); err != nil {
		t.Fatalf("initial status failed: %v", err)
	}
	if !st.Ready || st.Workspace != "/proj" {
		t.Fatalf("unexpected status: %+v", st)
	}

	// Verify X-Cage-Connection scoping.
	var cctx ClientContext
	if err := client.GetJSON(ctx, "/api/client/context", &cctx); err != nil {
		t.Fatalf("client/context failed: %v", err)
	}
	mu.Lock()
	if seenConn["/api/status"] != "conn-remote-1" {
		t.Fatalf("expected X-Cage-Connection on /api/status, got %q", seenConn["/api/status"])
	}
	if seenConn["/api/client/context"] != "" {
		t.Fatalf("unexpected X-Cage-Connection on /api/client/context: %q", seenConn["/api/client/context"])
	}
	mu.Unlock()

	// Verify 401 auto re-auth retry.
	forceUnauth.Store(true)
	st = StatusView{}
	if err := client.GetJSON(ctx, "/api/status", &st); err != nil {
		t.Fatalf("expected 401 auto re-auth retry to succeed, got: %v", err)
	}

	// Verify 423 Locked callback.
	forceLocked.Store(true)
	if err := client.GetJSON(ctx, "/api/status", &StatusView{}); err == nil {
		t.Fatal("expected HTTP 423 error")
	}
	if lockedCount.Load() != 1 {
		t.Fatalf("expected onLocked callback once, got %d", lockedCount.Load())
	}
}

func TestStorePreferencesPersistenceAndSnapshotIsolation(t *testing.T) {
	dir := t.TempDir()
	client := NewControllerClient(ClientOptions{ConfigPath: filepath.Join(dir, "config.json")})
	store := NewStore(client)

	var invalidated atomic.Int32
	store.SetInvalidate(func() { invalidated.Add(1) })
	store.UpdateClientPreferences(func(p *ClientPreferences) {
		p.AudioMuted = true
		p.AudioVolume = 0.5
		p.ThemeMode = "dark"
		p.SidebarWidth = 310
	})
	if invalidated.Load() == 0 {
		t.Fatal("expected SetInvalidate callback")
	}

	// Reload in a fresh Store using same config path.
	store2 := NewStore(NewControllerClient(ClientOptions{ConfigPath: filepath.Join(dir, "config.json")}))
	snap := store2.Snapshot()
	if !snap.ClientPrefs.AudioMuted || snap.ClientPrefs.ThemeMode != "dark" || snap.ClientPrefs.SidebarWidth != 310 || snap.ClientPrefs.AudioVolume != 0.5 {
		t.Fatalf("reloaded prefs mismatch: %+v", snap.ClientPrefs)
	}

	// Mutating returned snapshot map/slices must not corrupt internal store state.
	store2.SetUpdateState(map[string]any{"phase": "available"})
	s1 := store2.Snapshot()
	s1.Update["phase"] = "mutated"
	s2 := store2.Snapshot()
	if s2.Update["phase"] != "available" {
		t.Fatalf("snapshot Update map aliased internal state: %v", s2.Update)
	}
}

func TestPCMDecoderAudioRingBuffer(t *testing.T) {
	dec := NewPCMDecoder()
	if dec.SampleRate() != AudioSampleRate || dec.Channels() != AudioChannels || dec.Len() != -1 {
		t.Fatalf("unexpected decoder metadata: rate=%d ch=%d len=%d", dec.SampleRate(), dec.Channels(), dec.Len())
	}

	// Underrun produces silence rather than EOF while stream is open.
	out := make([]float32, 4)
	n, err := dec.Read(out)
	if err != nil || n != 4 {
		t.Fatalf("underrun read returned n=%d err=%v", n, err)
	}
	for i, s := range out {
		if s != 0 {
			t.Fatalf("expected silence at %d, got %v", i, s)
		}
	}

	// Encode two stereo frames: (+0.5, -0.5), (+0.25, -0.25)
	s0, s1, s2, s3 := int16(16384), int16(-16384), int16(8192), int16(-8192)
	pcm := make([]byte, 8)
	binary.LittleEndian.PutUint16(pcm[0:2], uint16(s0))
	binary.LittleEndian.PutUint16(pcm[2:4], uint16(s1))
	binary.LittleEndian.PutUint16(pcm[4:6], uint16(s2))
	binary.LittleEndian.PutUint16(pcm[6:8], uint16(s3))
	dec.WritePCM16LE(pcm)

	n, err = dec.Read(out)
	if err != nil || n != 4 {
		t.Fatalf("read failed: n=%d err=%v", n, err)
	}
	if math.Abs(float64(out[0]-0.5)) > 1e-4 || math.Abs(float64(out[1]+0.5)) > 1e-4 ||
		math.Abs(float64(out[2]-0.25)) > 1e-4 || math.Abs(float64(out[3]+0.25)) > 1e-4 {
		t.Fatalf("decoded float32 samples mismatch: %v", out)
	}

	// Latency cap drops excessive buffered audio instead of growing unbounded.
	chunk := make([]byte, AudioSampleRate*AudioChannels*2) // 1s = 2x capacity of 250ms target cap
	dec.WritePCM16LE(chunk)
	if q := dec.QueuedSamples(); q > audioMaxQueued {
		t.Fatalf("queued samples %d exceeded latency cap %d", q, audioMaxQueued)
	}

	// Muting flushes and outputs silence.
	dec.SetMuted(true)
	if dec.QueuedSamples() != 0 {
		t.Fatalf("expected muted queue cleared, got %d", dec.QueuedSamples())
	}

	dec.Close()
	if _, err := dec.Read(out); err != io.EOF {
		t.Fatalf("expected io.EOF after Close on empty queue, got %v", err)
	}
}

func TestAgentNativeLoopRoundTrip(t *testing.T) {
	var (
		mu        sync.Mutex
		completed map[string]any
		claimAuth string
	)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			http.SetCookie(w, &http.Cookie{Name: "cage_session_test", Value: "s1", Path: "/"})
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/launch":
			_ = json.NewEncoder(w).Encode(map[string]string{"url": srv.URL + "/#t2"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/agent/native/window":
			_, _ = w.Write([]byte(`{"window_proof":"win-proof-99"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/agent/native":
			mu.Lock()
			claimAuth = r.Header.Get("X-Cage-Agent-Window")
			already := completed != nil
			mu.Unlock()
			if already {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_ = json.NewEncoder(w).Encode(agentNativeItem{
				ID:         "req-1",
				Connection: "local",
				Status:     "running",
				Request:    json.RawMessage(`{"action":"clientPreferences","operation":"set","audio_muted":true,"theme_mode":"light"}`),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/agent/native/req-1/complete":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			completed = body
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.json")
	client := NewControllerClient(ClientOptions{
		ConfigPath: cfgPath,
		TestEntryFn: func(ctx context.Context) (DesktopResult, error) {
			return DesktopResult{Status: "ready", URL: srv.URL + "/#t1"}, nil
		},
	})
	if err := client.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	store := NewStore(client)
	bridge := &fakeHostBridge{}
	loop := NewAgentNativeLoop(client, store, NewClipboardSync(client), bridge)
	defer loop.clipboard.Stop()

	if err := loop.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if claimAuth != "win-proof-99" {
		t.Fatalf("expected X-Cage-Agent-Window=win-proof-99, got %q", claimAuth)
	}
	res, ok := completed["result"].(map[string]any)
	if !ok || res["audio_muted"] != true || res["theme_mode"] != "light" {
		t.Fatalf("unexpected completion payload: %+v", completed)
	}
}

func TestProxyStatusUnmarshalAndRunTrackedJob(t *testing.T) {
	// Verify real workbench /api/proxy schema (string revision, RFC3339 updated_at, string usage, skipped array)
	rawProxy := `{
		"active": false,
		"bootstrap_dns": "https://1.1.1.1/dns-query",
		"core_available": true,
		"ipv6_blocked": true,
		"network_open": false,
		"port": 17896,
		"revision": "",
		"route": [],
		"route_error": "",
		"running": false,
		"selected": "",
		"sources": [{
			"error": "",
			"host": "sub.example.com",
			"id": "src-1",
			"interval_hours": 24,
			"name": "默认订阅",
			"nodes": [{"id": "n1", "name": "Tokyo 01", "type": "anytls"}],
			"remote": true,
			"skipped": [{"index": 1, "name": "Bad Node", "type": "ss", "reason": "unsupported"}],
			"updated_at": "2026-11-18T14:27:50.984017+08:00",
			"usage": "upload=1822364516; download=3378458235; total=180388626432; expire=1802786704"
		}],
		"state": "unconfigured",
		"state_label": "尚未配置"
	}`
	var decoded ProxyStatusView
	if err := json.Unmarshal([]byte(rawProxy), &decoded); err != nil {
		t.Fatalf("unmarshal real /api/proxy schema: %v", err)
	}
	if len(decoded.Sources) != 1 || len(decoded.Sources[0].Nodes) != 1 || len(decoded.Sources[0].Skipped) != 1 {
		t.Fatalf("unexpected decoded proxy sources: %+v", decoded.Sources)
	}
	if decoded.Sources[0].Usage == "" || decoded.Sources[0].UpdatedAt.IsZero() {
		t.Fatalf("expected usage and updated_at populated, got %+v", decoded.Sources[0])
	}

	var statusCalls atomic.Int32
	var failNext atomic.Bool
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/launch":
			_ = json.NewEncoder(w).Encode(map[string]string{"url": srv.URL + "/#t2"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/agent/native/window":
			_, _ = w.Write([]byte(`{"window_proof":"wp1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/client/context":
			_ = json.NewEncoder(w).Encode(ClientContext{Mode: "local", ConnectionID: "local"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/account":
			_ = json.NewEncoder(w).Encode(AccountView{Exists: true, Unlocked: true})
		case r.Method == http.MethodGet && r.URL.Path == "/api/proxy":
			_, _ = w.Write([]byte(rawProxy))
		case r.Method == http.MethodPost && r.URL.Path == "/api/proxy/import":
			statusCalls.Store(0)
			jobID := "job-ok"
			if failNext.Load() {
				jobID = "job-err"
			}
			_ = json.NewEncoder(w).Encode(Job{ID: jobID, Kind: "proxy", Running: true, Message: "正在下载节点订阅…"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/status":
			n := statusCalls.Add(1)
			if failNext.Load() {
				_ = json.NewEncoder(w).Encode(StatusView{Job: Job{ID: "job-err", Kind: "proxy", Running: false, Error: "订阅需要 HTTPS"}})
				return
			}
			if n < 2 {
				_ = json.NewEncoder(w).Encode(StatusView{Job: Job{ID: "job-ok", Kind: "proxy", Running: true, Message: "正在验证节点配置…"}})
				return
			}
			_ = json.NewEncoder(w).Encode(StatusView{Job: Job{ID: "job-ok", Kind: "proxy", Running: false, Message: "已完成"}})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	client := NewControllerClient(ClientOptions{
		ConfigPath: filepath.Join(t.TempDir(), "config.json"),
		TestEntryFn: func(ctx context.Context) (DesktopResult, error) {
			return DesktopResult{Status: "ready", URL: srv.URL + "/#t1"}, nil
		},
	})
	store := NewStore(client)
	defer store.Stop()
	if err := store.BootstrapAndRefresh(context.Background()); err != nil {
		t.Fatalf("BootstrapAndRefresh: %v", err)
	}

	job, err := store.RunTrackedJob(context.Background(), "/api/proxy/import", map[string]any{"url": "https://sub.example.com"})
	if err != nil || job.ID != "job-ok" {
		t.Fatalf("expected tracked job success, got job=%+v err=%v", job, err)
	}
	if len(store.Snapshot().Proxy.Sources) != 1 || store.Snapshot().Proxy.Sources[0].Nodes[0].Name != "Tokyo 01" {
		t.Fatalf("expected refreshed proxy nodes in store, got %+v", store.Snapshot().Proxy)
	}

	failNext.Store(true)
	_, err = store.RunTrackedJob(context.Background(), "/api/proxy/import", map[string]any{"url": "http://invalid"})
	if err == nil || err.Error() != "订阅需要 HTTPS" {
		t.Fatalf("expected tracked job failure message, got err=%v", err)
	}
}

func TestHostPortConflictDetectionAndSuggestFreePort(t *testing.T) {
	if invalid := CheckLoopbackPort(80, false); invalid.State != PortStateInvalid {
		t.Fatalf("expected PortStateInvalid for privileged port 80, got %+v", invalid)
	}

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	occupiedPort := ln.Addr().(*net.TCPAddr).Port

	conflict := CheckLoopbackPort(occupiedPort, false)
	if conflict.State != PortStateConflict {
		t.Fatalf("expected PortStateConflict for occupied port %d, got %+v", occupiedPort, conflict)
	}
	selfOwned := CheckLoopbackPort(occupiedPort, true)
	if selfOwned.State != PortStateSelfRunning {
		t.Fatalf("expected PortStateSelfRunning when selfOwned=true, got %+v", selfOwned)
	}

	freePort, err := SuggestFreeProxyPort(17897, occupiedPort, false)
	if err != nil {
		t.Fatalf("SuggestFreeProxyPort failed: %v", err)
	}
	if freePort == occupiedPort || freePort < 1024 || freePort > 65535 {
		t.Fatalf("unexpected suggested port %d (occupied=%d)", freePort, occupiedPort)
	}
	if res := CheckLoopbackPort(freePort, false); res.State != PortStateAvailable {
		t.Fatalf("expected suggested port %d available, got %+v", freePort, res)
	}

	rep := InspectHostPorts(occupiedPort, Snapshot{
		Proxy: ProxyStatusView{Port: occupiedPort, Running: false},
	}, "http://127.0.0.1:49123")
	if rep.CandidateProxy.State != PortStateConflict {
		t.Fatalf("expected conflict candidate in report, got %+v", rep.CandidateProxy)
	}
	if len(rep.CageServices) < 3 || rep.CageServices[1].Address != "127.0.0.1:49123" {
		t.Fatalf("unexpected CageServices in report: %+v", rep.CageServices)
	}
	if rep.SuggestedPort < 1024 || rep.SuggestedPort == occupiedPort {
		t.Fatalf("unexpected SuggestedPort in report: %+v", rep)
	}
}

func TestStoreTerminalsArrayDecodingAndLocalMutations(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/session":
			http.SetCookie(w, &http.Cookie{Name: "cage_session_test", Value: "tok", Path: "/"})
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/launch":
			_ = json.NewEncoder(w).Encode(map[string]string{"url": srv.URL + "/#t1"})
		case "/api/agent/native/window":
			_, _ = w.Write([]byte(`{"window_proof":"wp"}`))
		case "/api/client/context":
			_ = json.NewEncoder(w).Encode(ClientContext{Mode: "local", ConnectionID: "local"})
		case "/api/account":
			_ = json.NewEncoder(w).Encode(AccountView{Exists: true, Unlocked: true, Username: "alice"})
		case "/api/environment/initialization":
			_ = json.NewEncoder(w).Encode(InitializationView{Required: false, Prepared: true})
		case "/api/status":
			_ = json.NewEncoder(w).Encode(StatusView{Ready: true, VMRunning: true, Workspace: "/proj"})
		case "/api/terminals":
			// Controller returns a bare JSON array, not an object wrapper.
			_ = json.NewEncoder(w).Encode([]TerminalSummary{
				{ID: "term-1", Title: "终端 1", Workspace: "/proj", Kind: "shell", Closed: false},
				{ID: "term-2", Title: "终端 2", Workspace: "/proj", Kind: "shell", Closed: true},
			})
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	client := NewControllerClient(ClientOptions{
		ConfigPath: filepath.Join(t.TempDir(), "config.json"),
		TestEntryFn: func(ctx context.Context) (DesktopResult, error) {
			return DesktopResult{Status: "ready", URL: srv.URL + "/#t1"}, nil
		},
	})
	store := NewStore(client)
	defer store.Stop()
	if err := store.BootstrapAndRefresh(context.Background()); err != nil {
		t.Fatalf("BootstrapAndRefresh: %v", err)
	}

	got := store.Snapshot().Terminals
	if len(got) != 2 || got[0].ID != "term-1" || got[1].ID != "term-2" || !got[1].Closed {
		t.Fatalf("expected 2 decoded terminals from array response, got %+v", got)
	}

	store.UpsertTerminal(TerminalSummary{ID: "term-3", Title: "终端 3", Workspace: "/proj", Kind: "shell"})
	if len(store.Snapshot().Terminals) != 3 {
		t.Fatalf("expected 3 terminals after UpsertTerminal, got %+v", store.Snapshot().Terminals)
	}
	store.RemoveTerminal("term-2")
	afterRemove := store.Snapshot().Terminals
	if len(afterRemove) != 2 || afterRemove[0].ID != "term-1" || afterRemove[1].ID != "term-3" {
		t.Fatalf("unexpected terminals after RemoveTerminal: %+v", afterRemove)
	}
}
