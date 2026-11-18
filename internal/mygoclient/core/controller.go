package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sylvinus/agent-vm/internal/macnative"
)

const (
	connectionHeader = "X-Cage-Connection"
	configEnv        = "CLAUDE_CAGE_CONFIG"
)

// HTTPError represents a non-2xx JSON/text response from the workbench backend.
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

type ClientOptions struct {
	Executable string
	ConfigPath string
	ClientOnly bool
	// Test hooks allow headless integration tests against httptest servers.
	TestEntryFn func(ctx context.Context) (DesktopResult, error)
}

type ControllerClient struct {
	opts ClientOptions

	mu           sync.RWMutex
	exe          string
	configPath   string
	clientOnly   bool
	origin       string
	host         string
	baseURL      *url.URL
	connectionID string
	windowProof  string
	controller   ControllerIdentity
	httpClient   *http.Client
	jar          *cookiejar.Jar
	authMu       sync.Mutex
	onLocked     func()
}

func NewControllerClient(opts ClientOptions) *ControllerClient {
	jar, _ := cookiejar.New(nil)
	prof := macnative.DetectProfile()
	cfg := strings.TrimSpace(opts.ConfigPath)
	if cfg == "" {
		cfg = prof.ConfigPath
	}
	clientOnly := opts.ClientOnly || prof.ClientOnly
	return &ControllerClient{
		opts:       opts,
		configPath: cfg,
		clientOnly: clientOnly,
		jar:        jar,
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
		},
	}
}

func (c *ControllerClient) SetOnLocked(fn func()) {
	c.mu.Lock()
	c.onLocked = fn
	c.mu.Unlock()
}

func (c *ControllerClient) ConfigPath() string {
	return c.configPath
}

func (c *ControllerClient) ProfileDir() string {
	return filepath.Dir(c.configPath)
}

func (c *ControllerClient) ClientOnly() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.clientOnly
}

func (c *ControllerClient) Origin() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.origin
}

func (c *ControllerClient) Host() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.host
}

func (c *ControllerClient) ConnectionID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connectionID
}

func (c *ControllerClient) SetConnectionID(id string) {
	c.mu.Lock()
	c.connectionID = strings.TrimSpace(id)
	c.mu.Unlock()
}

func (c *ControllerClient) WindowProof() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.windowProof
}

func (c *ControllerClient) ControllerIdentity() ControllerIdentity {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.controller
}

func (c *ControllerClient) Cookies() []*http.Cookie {
	c.mu.RLock()
	u := c.baseURL
	c.mu.RUnlock()
	if u == nil {
		return nil
	}
	return c.jar.Cookies(u)
}

// ResolveExecutable locates the bundled or workspace cage / cage-client binary.
func (c *ControllerClient) ResolveExecutable() (string, error) {
	c.mu.RLock()
	if c.exe != "" {
		exe := c.exe
		c.mu.RUnlock()
		return exe, nil
	}
	c.mu.RUnlock()

	if c.opts.Executable != "" {
		if _, err := os.Stat(c.opts.Executable); err == nil {
			c.mu.Lock()
			c.exe = c.opts.Executable
			c.mu.Unlock()
			return c.opts.Executable, nil
		}
		return "", fmt.Errorf("未找到指定的后端程序：%s", c.opts.Executable)
	}

	name := "cage"
	if c.clientOnly {
		name = "cage-client"
	}

	var candidates []string
	if prof := macnative.DetectProfile(); prof.BundlePath != "" {
		// Both Cage.app and Cage Client.app bundle their backend helper as Contents/Resources/cage.
		candidates = append(candidates, filepath.Join(prof.BundlePath, "Contents", "Resources", "cage"))
		if name != "cage" {
			candidates = append(candidates, filepath.Join(prof.BundlePath, "Contents", "Resources", name))
		}
	}
	if cageExe, err := macnative.CageBinaryPath(); err == nil && cageExe != "" && !c.clientOnly {
		candidates = append(candidates, cageExe)
	}
	if self, err := os.Executable(); err == nil {
		dir := filepath.Dir(self)
		candidates = append(candidates,
			filepath.Join(dir, "..", "Resources", "cage"),
			filepath.Join(dir, "..", "Resources", name),
			filepath.Join(dir, name),
			filepath.Join(dir, "_output", "bin", name),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		cur := cwd
		for i := 0; i < 4; i++ {
			candidates = append(candidates,
				filepath.Join(cur, "_output", "bin", name),
				filepath.Join(cur, "build", "Cage.app", "Contents", "Resources", name),
			)
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(p)
			if abs == "" {
				abs = p
			}
			c.mu.Lock()
			c.exe = abs
			c.mu.Unlock()
			return abs, nil
		}
	}
	if found, err := exec.LookPath(name); err == nil {
		c.mu.Lock()
		c.exe = found
		c.mu.Unlock()
		return found, nil
	}
	return "", fmt.Errorf("未找到 %s 后端可执行文件，请重新构建应用或检查安装完整性", name)
}

// Bootstrap starts or attaches to the Cage controller and authenticates the HTTP session.
func (c *ControllerClient) Bootstrap(ctx context.Context) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	return c.bootstrapLocked(ctx)
}

// IssueLaunchTicket returns a fresh, unconsumed one-time session ticket for an
// embedded WKWebView so it can establish its own HttpOnly session cookie via /api/session.
func (c *ControllerClient) IssueLaunchTicket(ctx context.Context) (string, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.Origin() == "" {
		if err := c.bootstrapLocked(ctx); err != nil {
			return "", err
		}
	}
	if c.opts.TestEntryFn != nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+"/api/launch", bytes.NewReader([]byte(`{}`)))
		if err != nil {
			return "", err
		}
		req.Host = c.host
		req.Header.Set("Origin", c.origin)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", &HTTPError{StatusCode: resp.StatusCode, Message: "无法签发工作台会话凭证"}
		}
		var launch struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&launch); err != nil || launch.URL == "" {
			return "", errors.New("工作台会话凭证响应无效")
		}
		_, _, _, ticket, err := parseLaunchURL(launch.URL)
		return ticket, err
	}

	entry, err := c.runDesktopEntry(ctx, false)
	if err != nil {
		return "", err
	}
	_, _, _, ticket, err := parseLaunchURL(entry.URL)
	if err != nil {
		return "", err
	}
	return ticket, nil
}

func (c *ControllerClient) bootstrapLocked(ctx context.Context) error {
	var entry DesktopResult
	var err error
	if c.opts.TestEntryFn != nil {
		entry, err = c.opts.TestEntryFn(ctx)
	} else {
		entry, err = c.runDesktopEntry(ctx, false)
	}
	if err != nil {
		return err
	}
	if entry.Status != "ready" && entry.Status != "existing" && entry.URL == "" {
		msg := entry.Message
		if msg == "" {
			msg = "Cage 后台服务未能启动"
		}
		return errors.New(msg)
	}
	origin, host, baseURL, ticket, err := parseLaunchURL(entry.URL)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.origin = origin
	c.host = host
	c.baseURL = baseURL
	c.controller = entry.Controller
	c.mu.Unlock()

	if ticket != "" {
		if err := c.exchangeSessionLocked(ctx, ticket); err != nil {
			return err
		}
	}
	_ = c.registerNativeWindowLocked(ctx)
	return nil
}

func (c *ControllerClient) runDesktopEntry(ctx context.Context, existing bool) (DesktopResult, error) {
	exe, err := c.ResolveExecutable()
	if err != nil {
		return DesktopResult{}, err
	}
	args := []string{"desktop", "--json"}
	if existing {
		args = append(args, "--existing")
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, exe, args...)
	cmd.Env = filteredBackendEnv(c.configPath)
	out, err := cmd.CombinedOutput()
	var res DesktopResult
	if trimmed := bytes.TrimSpace(out); len(trimmed) > 0 {
		_ = json.Unmarshal(trimmed, &res)
	}
	if err != nil {
		if res.Message != "" {
			return res, errors.New(res.Message)
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return res, fmt.Errorf("启动 Cage 服务失败：%s", msg)
	}
	return res, nil
}

func parseLaunchURL(raw string) (origin, host string, parsed *url.URL, ticket string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", nil, "", fmt.Errorf("无效的工作台地址：%q", raw)
	}
	frag := strings.TrimSpace(u.Fragment)
	for _, prefix := range []string{"ticket=", "token="} {
		if strings.HasPrefix(frag, prefix) {
			frag = strings.TrimPrefix(frag, prefix)
			break
		}
	}
	origin = u.Scheme + "://" + u.Host
	base := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/"}
	return origin, u.Host, base, frag, nil
}

func (c *ControllerClient) exchangeSessionLocked(ctx context.Context, ticket string) error {
	payload, _ := json.Marshal(map[string]string{"token": ticket})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+"/api/session", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Host = c.host
	req.Header.Set("Origin", c.origin)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("建立工作台会话失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = fmt.Sprintf("会话认证失败 (%d)", resp.StatusCode)
		}
		return &HTTPError{StatusCode: resp.StatusCode, Message: msg}
	}
	return nil
}

func (c *ControllerClient) registerNativeWindowLocked(ctx context.Context) error {
	var ticket2 string
	if c.opts.TestEntryFn != nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+"/api/launch", bytes.NewReader([]byte(`{}`)))
		if err != nil {
			return err
		}
		req.Host = c.host
		req.Header.Set("Origin", c.origin)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil
		}
		var launch struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&launch); err != nil || launch.URL == "" {
			return err
		}
		origin2, _, _, t2, err := parseLaunchURL(launch.URL)
		if err != nil || origin2 != c.origin || t2 == "" {
			return nil
		}
		ticket2 = t2
	} else {
		entry, err := c.runDesktopEntry(ctx, false)
		if err != nil || entry.URL == "" {
			return err
		}
		origin2, _, _, t2, err := parseLaunchURL(entry.URL)
		if err != nil || origin2 != c.origin || t2 == "" {
			return nil
		}
		ticket2 = t2
	}
	payload, _ := json.Marshal(map[string]string{"ticket": ticket2})
	wreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+"/api/agent/native/window", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	wreq.Host = c.host
	wreq.Header.Set("Origin", c.origin)
	wreq.Header.Set("Content-Type", "application/json")
	wresp, err := c.httpClient.Do(wreq)
	if err != nil {
		return err
	}
	defer wresp.Body.Close()
	if wresp.StatusCode < 200 || wresp.StatusCode >= 300 {
		return nil
	}
	var grant struct {
		WindowProof string `json:"window_proof"`
	}
	if json.NewDecoder(io.LimitReader(wresp.Body, 8<<10)).Decode(&grant) == nil && grant.WindowProof != "" {
		c.mu.Lock()
		c.windowProof = grant.WindowProof
		c.mu.Unlock()
	}
	return nil
}

func (c *ControllerClient) reauthenticate(ctx context.Context) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.opts.TestEntryFn != nil {
		return c.bootstrapLocked(ctx)
	}
	entry, err := c.runDesktopEntry(ctx, false)
	if err != nil || entry.URL == "" {
		return c.bootstrapLocked(ctx)
	}
	origin, host, baseURL, ticket, parseErr := parseLaunchURL(entry.URL)
	if parseErr != nil {
		return c.bootstrapLocked(ctx)
	}
	c.mu.Lock()
	c.origin = origin
	c.host = host
	c.baseURL = baseURL
	c.controller = entry.Controller
	c.mu.Unlock()
	if ticket != "" {
		if err := c.exchangeSessionLocked(ctx, ticket); err != nil {
			return err
		}
	}
	_ = c.registerNativeWindowLocked(ctx)
	return nil
}

func shouldScopeConnection(path string) bool {
	p := path
	if idx := strings.IndexByte(p, '?'); idx >= 0 {
		p = p[:idx]
	}
	if strings.HasPrefix(p, "/api/client/") ||
		p == "/api/session" ||
		p == "/api/launch" ||
		p == "/api/handoff" ||
		p == "/api/agent/native/window" {
		return false
	}
	return strings.HasPrefix(p, "/api/")
}

func (c *ControllerClient) prepareRequest(ctx context.Context, method, path string, body []byte, contentType string) (*http.Request, error) {
	c.mu.RLock()
	origin := c.origin
	host := c.host
	connID := c.connectionID
	c.mu.RUnlock()
	if origin == "" {
		return nil, errors.New("工作台尚未连接到 Cage 服务")
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, origin+path, reader)
	if err != nil {
		return nil, err
	}
	req.Host = host
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", origin)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if connID != "" && shouldScopeConnection(path) {
		req.Header.Set(connectionHeader, connID)
	}
	return req, nil
}

func (c *ControllerClient) DoRaw(ctx context.Context, method, path string, body []byte, contentType string) ([]byte, http.Header, error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := c.prepareRequest(ctx, method, path, body, contentType)
		if err != nil {
			return nil, nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt == 0 && ctx.Err() == nil && c.reauthenticate(ctx) == nil {
				continue
			}
			return nil, nil, err
		}
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, nil, readErr
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			if err := c.reauthenticate(ctx); err == nil {
				continue
			}
		}
		if resp.StatusCode == http.StatusLocked {
			c.mu.RLock()
			lockedFn := c.onLocked
			c.mu.RUnlock()
			if lockedFn != nil {
				lockedFn()
			}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			msg := strings.TrimSpace(string(respBody))
			return respBody, resp.Header, &HTTPError{StatusCode: resp.StatusCode, Message: msg}
		}
		return respBody, resp.Header, nil
	}
	return nil, nil, errors.New("会话已失效")
}

func (c *ControllerClient) GetJSON(ctx context.Context, path string, out any) error {
	return c.RequestJSON(ctx, http.MethodGet, path, nil, out)
}

func (c *ControllerClient) PostJSON(ctx context.Context, path string, in any, out any) error {
	return c.RequestJSON(ctx, http.MethodPost, path, in, out)
}

func (c *ControllerClient) PutJSON(ctx context.Context, path string, in any, out any) error {
	return c.RequestJSON(ctx, http.MethodPut, path, in, out)
}

func (c *ControllerClient) DeleteJSON(ctx context.Context, path string, out any) error {
	return c.RequestJSON(ctx, http.MethodDelete, path, nil, out)
}

func (c *ControllerClient) RequestJSON(ctx context.Context, method, path string, in any, out any) error {
	var body []byte
	var ct string
	if in != nil {
		var err error
		body, err = json.Marshal(in)
		if err != nil {
			return err
		}
		ct = "application/json"
	} else if method != http.MethodGet && method != http.MethodHead {
		body = []byte(`{}`)
		ct = "application/json"
	}
	raw, _, err := c.DoRaw(ctx, method, path, body, ct)
	if err != nil {
		return err
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("解析响应 %s 失败：%w", path, err)
		}
	}
	return nil
}

func (c *ControllerClient) PostMultipart(ctx context.Context, path, fieldName, fileName string, fileData []byte, fields map[string]string, out any) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if fieldName != "" {
		fw, err := mw.CreateFormFile(fieldName, fileName)
		if err != nil {
			return err
		}
		if _, err := fw.Write(fileData); err != nil {
			return err
		}
	}
	if err := mw.Close(); err != nil {
		return err
	}
	raw, _, err := c.DoRaw(ctx, http.MethodPost, path, buf.Bytes(), mw.FormDataContentType())
	if err != nil {
		return err
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// PostMultipartStream streams a host file without loading large binaries into a single buffer when possible.
func (c *ControllerClient) PostMultipartStream(ctx context.Context, path, fieldName, filePath string, out any) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	return c.PostMultipart(ctx, path, fieldName, filepath.Base(filePath), data, nil, out)
}

func (c *ControllerClient) OpenSSE(ctx context.Context, path string) (*http.Response, error) {
	req, err := c.prepareRequest(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	sseClient := &http.Client{Jar: c.jar}
	resp, err := sseClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if err := c.reauthenticate(ctx); err != nil {
			return nil, err
		}
		req, err = c.prepareRequest(ctx, http.MethodGet, path, nil, "")
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")
		resp, err = sseClient.Do(req)
		if err != nil {
			return nil, err
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		resp.Body.Close()
		return nil, &HTTPError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(body))}
	}
	return resp, nil
}

func (c *ControllerClient) DialWebSocket(ctx context.Context, pathWithQuery string, enableCompression bool) (*websocket.Conn, *http.Response, error) {
	dialOnce := func() (*websocket.Conn, *http.Response, error) {
		c.mu.RLock()
		origin := c.origin
		host := c.host
		baseURL := c.baseURL
		connID := c.connectionID
		c.mu.RUnlock()
		if origin == "" || baseURL == nil {
			return nil, nil, errors.New("工作台尚未连接")
		}
		wsScheme := "ws"
		if baseURL.Scheme == "https" {
			wsScheme = "wss"
		}
		wsURL := wsScheme + "://" + host + pathWithQuery
		hdr := http.Header{}
		hdr.Set("Origin", origin)
		if connID != "" && shouldScopeConnection(pathWithQuery) {
			hdr.Set(connectionHeader, connID)
		}
		if cookies := c.jar.Cookies(baseURL); len(cookies) > 0 {
			parts := make([]string, 0, len(cookies))
			for _, ck := range cookies {
				parts = append(parts, ck.Name+"="+ck.Value)
			}
			hdr.Set("Cookie", strings.Join(parts, "; "))
		}
		dialer := websocket.Dialer{
			HandshakeTimeout:  15 * time.Second,
			EnableCompression: enableCompression,
		}
		return dialer.DialContext(ctx, wsURL, hdr)
	}
	ws, resp, err := dialOnce()
	if err != nil && resp != nil && resp.StatusCode == http.StatusUnauthorized {
		if c.reauthenticate(ctx) == nil {
			return dialOnce()
		}
	}
	return ws, resp, err
}

func (c *ControllerClient) PrepareUpdate(ctx context.Context, stop bool) (DesktopResult, error) {
	exe, err := c.ResolveExecutable()
	if err != nil {
		return DesktopResult{}, err
	}
	args := []string{"prepare-update", "--json"}
	if stop {
		args = append(args, "--stop")
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = filteredBackendEnv(c.configPath)
	out, err := cmd.CombinedOutput()
	var res DesktopResult
	if trimmed := bytes.TrimSpace(out); len(trimmed) > 0 {
		_ = json.Unmarshal(trimmed, &res)
	}
	if err != nil {
		if res.Message != "" {
			return res, errors.New(res.Message)
		}
		return res, fmt.Errorf("准备更新失败：%v", err)
	}
	return res, nil
}

func (c *ControllerClient) RunFileCommand(ctx context.Context, subcmd string, args ...string) (map[string]any, error) {
	exe, err := c.ResolveExecutable()
	if err != nil {
		return nil, err
	}
	c.mu.RLock()
	connID := c.connectionID
	c.mu.RUnlock()
	if connID == "" {
		connID = "local"
	}
	full := []string{subcmd, "--json"}
	switch subcmd {
	case "diagnostics":
		if connID != "" && connID != "local" {
			full = append(full, "--connection", connID)
		}
	default:
		full = append(full, "--connection", connID)
	}
	full = append(full, args...)
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, exe, full...)
	cmd.Env = filteredBackendEnv(c.configPath)
	out, runErr := cmd.Output()
	var parsed map[string]any
	if len(bytes.TrimSpace(out)) > 0 {
		_ = json.Unmarshal(out, &parsed)
	}
	if runErr != nil {
		if msg, _ := parsed["message"].(string); msg != "" {
			return nil, errors.New(msg)
		}
		if ee, ok := runErr.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, errors.New(strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, runErr
	}
	if parsed == nil {
		return map[string]any{"status": "ok"}, nil
	}
	return parsed, nil
}

func filteredBackendEnv(configPath string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+2)
	for _, entry := range env {
		if strings.HasPrefix(entry, configEnv+"=") || strings.HasPrefix(entry, "CAGE_DESKTOP_OWNER_PID=") {
			continue
		}
		out = append(out, entry)
	}
	if configPath != "" {
		out = append(out, configEnv+"="+configPath)
	}
	out = append(out, "CAGE_DESKTOP_OWNER_PID="+strconv.Itoa(os.Getpid()))
	return out
}
