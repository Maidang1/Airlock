package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/guestos"
	"github.com/sylvinus/agent-vm/internal/herdrbridge"
	"github.com/sylvinus/agent-vm/internal/host"
	"github.com/sylvinus/agent-vm/internal/paths"
)

const herdrBridgeGuestPort = 18520

type herdrBridgeRuntime struct {
	manager    *herdrbridge.Manager
	listener   net.Listener
	httpServer *http.Server
	hostPort   int
	guestPort  int
	token      string
	cancel     context.CancelFunc
	done       chan struct{}

	mu              sync.Mutex
	tunnelConnected bool
	tunnelError     string
}

func (s *Server) herdrEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closing && s.preferences.HerdrSharedAgents
}

func (s *Server) syncHerdrBridge() {
	if !s.herdrEnabled() {
		s.stopHerdrBridge()
		return
	}
	_, _ = s.ensureHerdrBridge()
}

func (s *Server) ensureHerdrBridge() (*herdrBridgeRuntime, error) {
	s.herdrMu.Lock()
	defer s.herdrMu.Unlock()
	if s.herdr != nil {
		return s.herdr, nil
	}
	cfg, err := herdrbridge.LoadConfig(s.profile)
	if err != nil {
		diagnostics.Log("error", "herdr", "load herdr-agents config: "+err.Error())
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	hostPort := listener.Addr().(*net.TCPAddr).Port
	token := randomToken()
	runner := herdrbridge.NewAutoRunner(cfg)
	manager := herdrbridge.NewManager(cfg, runner, s.resolveHerdrWorkdir)
	handler := herdrbridge.NewServer(manager, token)

	ctx, cancel := context.WithCancel(s.lifetimeContext())
	rt := &herdrBridgeRuntime{
		manager:    manager,
		listener:   listener,
		httpServer: &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second},
		hostPort:   hostPort,
		guestPort:  herdrBridgeGuestPort,
		token:      token,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	go func() {
		_ = rt.httpServer.Serve(listener)
	}()
	go func() {
		defer close(rt.done)
		rt.keepTunnel(ctx, s)
	}()
	s.herdr = rt
	return rt, nil
}

func (s *Server) stopHerdrBridge() {
	s.herdrMu.Lock()
	rt := s.herdr
	s.herdr = nil
	s.herdrMu.Unlock()
	if rt == nil {
		return
	}
	rt.cancel()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	rt.manager.CloseAll(shutdownCtx)
	_ = rt.httpServer.Shutdown(shutdownCtx)
	_ = rt.httpServer.Close()
	_ = rt.listener.Close()
	<-rt.done
}

func (s *Server) herdrMCPPayload() map[string]string {
	if !s.herdrEnabled() {
		return nil
	}
	rt, err := s.ensureHerdrBridge()
	if err != nil {
		diagnostics.Log("warn", "herdr", "herdr bridge unavailable: "+err.Error())
		return nil
	}
	return map[string]string{
		"url":   fmt.Sprintf("http://127.0.0.1:%d/mcp", rt.guestPort),
		"token": rt.token,
	}
}

func (s *Server) herdrMCPServerConfig() map[string]any {
	payload := s.herdrMCPPayload()
	if payload == nil {
		return nil
	}
	return map[string]any{
		"type": "http",
		"url":  payload["url"],
		"headers": map[string]string{
			"Authorization": "Bearer " + payload["token"],
		},
	}
}

func (s *Server) herdrMCPConfigArg() string {
	entry := s.herdrMCPServerConfig()
	if entry == nil {
		return ""
	}
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"herdr": entry,
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *Server) herdrBridgeStatus(w http.ResponseWriter, r *http.Request) {
	cfg, _ := herdrbridge.LoadConfig(s.profile)
	hostStatus := herdrbridge.DetectHost(r.Context(), cfg)
	enabled := s.herdrEnabled()
	if !enabled {
		jsonReply(w, map[string]any{
			"enabled":          false,
			"running":          false,
			"tunnel_connected": false,
			"guest_port":       herdrBridgeGuestPort,
			"config_path":      cfg.ConfigPath,
			"host":             hostStatus,
			"profiles":         []any{},
			"active_sessions":  []any{},
		})
		return
	}
	rt, err := s.ensureHerdrBridge()
	if err != nil {
		requestError(w, err)
		return
	}
	rt.mu.Lock()
	tunnelConnected := rt.tunnelConnected
	tunnelError := rt.tunnelError
	rt.mu.Unlock()
	summary := rt.manager.ListAgents(r.Context())
	jsonReply(w, map[string]any{
		"enabled":          true,
		"running":          true,
		"host_port":        rt.hostPort,
		"guest_port":       rt.guestPort,
		"tunnel_connected": tunnelConnected,
		"tunnel_error":     tunnelError,
		"herdr_available":  summary.HerdrAvailable,
		"herdr_error":      summary.HerdrError,
		"backend":          summary.Backend,
		"binary_path":      summary.BinaryPath,
		"hc_path":          summary.HCPath,
		"version":          summary.Version,
		"socket_path":      summary.SocketPath,
		"config_path":      summary.ConfigPath,
		"host":             hostStatus,
		"profiles":         summary.Profiles,
		"active_sessions":  summary.ActiveSessions,
	})
}

func (s *Server) herdrBridgeInstall(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Method      string `json:"method"`
		StartServer *bool  `json:"start_server"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	method := strings.TrimSpace(strings.ToLower(input.Method))
	if method == "" {
		method = "auto"
	}
	if method != "auto" && method != "brew" && method != "script" {
		http.Error(w, "不支持的 Herdr 安装方式，请选择 auto、brew 或 script", http.StatusBadRequest)
		return
	}
	startServer := true
	if input.StartServer != nil {
		startServer = *input.StartServer
	}
	s.launch(w, "herdr-install", func() error {
		cfg, err := herdrbridge.LoadConfig(s.profile)
		if err != nil {
			return err
		}
		if _, err := herdrbridge.InstallHost(s.operationContext(), cfg, method, startServer, s.setMessage); err != nil {
			return err
		}
		s.syncHerdrBridge()
		return nil
	})
}

func (s *Server) herdrBridgeStartServer(w http.ResponseWriter, r *http.Request) {
	s.launch(w, "herdr-start", func() error {
		cfg, err := herdrbridge.LoadConfig(s.profile)
		if err != nil {
			return err
		}
		if _, err := herdrbridge.StartHostServer(s.operationContext(), cfg, s.setMessage); err != nil {
			return err
		}
		s.syncHerdrBridge()
		return nil
	})
}

func (s *Server) herdrBridgeCloseSession(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SessionID string `json:"session_id"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	s.herdrMu.Lock()
	rt := s.herdr
	s.herdrMu.Unlock()
	if rt == nil {
		http.Error(w, "Herdr 桥接尚未启用", http.StatusConflict)
		return
	}
	view, err := rt.manager.CloseSession(r.Context(), input.SessionID)
	if err != nil {
		requestError(w, err)
		return
	}
	jsonReply(w, view)
}

func (rt *herdrBridgeRuntime) setTunnelState(connected bool, errMsg string) {
	rt.mu.Lock()
	rt.tunnelConnected = connected
	rt.tunnelError = errMsg
	rt.mu.Unlock()
}

func (rt *herdrBridgeRuntime) keepTunnel(ctx context.Context, s *Server) {
	for ctx.Err() == nil {
		if s.windowsGuest != nil {
			rt.setTunnelState(false, "Windows 客机暂不支持反向 SSH 桥接")
			if !sleepContext(ctx, 5*time.Second) {
				return
			}
			continue
		}
		if !s.vmRunning() {
			rt.setTunnelState(false, "客机未运行")
			if !sleepContext(ctx, 2*time.Second) {
				return
			}
			continue
		}

		cmd := s.herdrTunnelCommand(ctx, rt.guestPort, rt.hostPort)
		if cmd.Err != nil {
			rt.setTunnelState(false, cmd.Err.Error())
			if !sleepContext(ctx, 2*time.Second) {
				return
			}
			continue
		}
		if err := cmd.Start(); err != nil {
			rt.setTunnelState(false, err.Error())
			if !sleepContext(ctx, 2*time.Second) {
				return
			}
			continue
		}

		waitDone := make(chan error, 1)
		go func() {
			waitDone <- cmd.Wait()
		}()

		// Confirm ExitOnForwardFailure did not immediately reject the tunnel.
		timer := time.NewTimer(300 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			<-waitDone
			rt.setTunnelState(false, "")
			return
		case err := <-waitDone:
			timer.Stop()
			msg := "tunnel exited"
			if err != nil {
				msg = err.Error()
			}
			rt.setTunnelState(false, msg)
		case <-timer.C:
			rt.setTunnelState(true, "")
			select {
			case <-ctx.Done():
				<-waitDone
				rt.setTunnelState(false, "")
				return
			case err := <-waitDone:
				msg := "tunnel disconnected"
				if err != nil {
					msg = err.Error()
				}
				rt.setTunnelState(false, msg)
			}
		}

		if !sleepContext(ctx, 2*time.Second) {
			return
		}
	}
}

func (s *Server) herdrTunnelCommand(ctx context.Context, guestPort, hostPort int) *exec.Cmd {
	if s.macosGuest != nil {
		args, err := guestos.MacOSRemoteForwardSSHArgs(s.profile, s.macosGuest.User, guestPort, hostPort)
		if err != nil {
			return &exec.Cmd{Err: err}
		}
		cmd := exec.CommandContext(ctx, sshExecutable(), args...)
		cmd.Env = sshEnvironment()
		host.HideCommandWindow(cmd)
		return cmd
	}
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", guestPort, hostPort)
	return s.sshCommandWithForwarding(ctx, "-N", "-o", "ExitOnForwardFailure=yes", "-R", forward, s.sshTarget())
}

func (s *Server) resolveHerdrWorkdir(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || strings.ContainsRune(requested, '\x00') {
		return "", errors.New("workdir is required")
	}
	roots, err := s.authorizedHostProjectRoots()
	if err != nil {
		return "", err
	}
	if len(roots) == 0 {
		return "", errors.New("no authorized Cage shared projects are configured")
	}

	cleanedSlash := path.Clean(filepath.ToSlash(requested))
	if !strings.HasPrefix(cleanedSlash, "/") {
		return "", errors.New("workdir must be an absolute path")
	}

	// 1. Match guest-side project paths (Linux guestPath or macOS Cage/Projects mount).
	for _, hostRoot := range roots {
		guestRoots := []string{s.cliWorkspace(hostRoot)}
		if s.macosGuest == nil {
			if gp := guestPath(hostRoot); gp != guestRoots[0] {
				guestRoots = append(guestRoots, gp)
			}
		}
		for _, guestRoot := range guestRoots {
			guestRoot = path.Clean(filepath.ToSlash(guestRoot))
			if rel, ok := slashRelativeSubpath(guestRoot, cleanedSlash); ok {
				candidate := hostRoot
				if rel != "." {
					candidate = filepath.Join(hostRoot, filepath.FromSlash(rel))
				}
				return validateResolvedHostSubdir(hostRoot, candidate)
			}
		}
	}

	// 2. Match host-side paths within authorized project roots.
	for _, hostRoot := range roots {
		if resolved, err := validateResolvedHostSubdir(hostRoot, requested); err == nil {
			return resolved, nil
		}
	}

	return "", fmt.Errorf("workdir %q is outside authorized Cage shared projects", requested)
}

func (s *Server) authorizedHostProjectRoots() ([]string, error) {
	listed, err := s.projectPaths()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var roots []string
	add := func(dir string) {
		if dir == "" || isClientMountPath(dir) {
			return
		}
		resolved, err := paths.Resolve(dir)
		if err != nil {
			return
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return
		}
		if !seen[resolved] {
			seen[resolved] = true
			roots = append(roots, resolved)
		}
	}
	for _, p := range listed {
		add(p)
		if folders, err := s.projectFolders(p); err == nil {
			for _, f := range folders {
				add(f)
			}
		}
	}
	return roots, nil
}

func validateResolvedHostSubdir(hostRoot, candidate string) (string, error) {
	resolvedRoot, err := paths.Resolve(hostRoot)
	if err != nil {
		return "", err
	}
	resolvedTarget, err := paths.Resolve(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("workdir escapes authorized project root")
	}
	info, err := os.Stat(resolvedTarget)
	if err != nil || !info.IsDir() {
		return "", errors.New("workdir is not an existing directory")
	}
	return resolvedTarget, nil
}

func slashRelativeSubpath(root, target string) (string, bool) {
	if target == root {
		return ".", true
	}
	prefix := strings.TrimSuffix(root, "/") + "/"
	if strings.HasPrefix(target, prefix) {
		rel := strings.TrimPrefix(target, prefix)
		if rel == "" {
			return ".", true
		}
		return rel, true
	}
	return "", false
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
