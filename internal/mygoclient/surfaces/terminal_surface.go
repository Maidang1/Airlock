package surfaces

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/gorilla/websocket"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

var (
	ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	urlFinderRe  = regexp.MustCompile(`https://[^\s"'<>` + "`" + `\x00-\x1f\x7f]+`)
)

// IsClaudeAuthURL matches Anthropic / Claude OAuth authorize URLs handled by /api/terminals/{id}/login-link.
func IsClaudeAuthURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 8192 || strings.ContainsAny(raw, "\r\n\t \\") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery == "" {
		return false
	}
	p := strings.TrimSuffix(u.EscapedPath(), "/")
	if p != "/oauth/authorize" && p != "/cai/oauth/authorize" {
		return false
	}
	switch strings.ToLower(u.Host) {
	case "claude.ai", "claude.com", "console.anthropic.com", "platform.claude.com", "platform.anthropic.com":
		return true
	}
	return false
}

// ExtractClaudeAuthURL scans terminal output bytes (stripping ANSI escapes) for an OAuth login URL.
func ExtractClaudeAuthURL(tailBuf *strings.Builder, chunk []byte) string {
	clean := ansiEscapeRe.ReplaceAllString(string(chunk), "")
	// Terminal wraps long URLs across lines; collapse hard CRLF/LF wraps cautiously when scanning tail.
	tailBuf.WriteString(clean)
	if tailBuf.Len() > 16384 {
		s := tailBuf.String()
		tailBuf.Reset()
		tailBuf.WriteString(s[len(s)-8192:])
	}
	text := strings.ReplaceAll(strings.ReplaceAll(tailBuf.String(), "\r\n", ""), "\n", "")
	matches := urlFinderRe.FindAllString(text, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		cand := strings.TrimRight(matches[i], ".,;:!?)")
		if IsClaudeAuthURL(cand) {
			return cand
		}
	}
	return ""
}

type wsTerminalConn struct {
	id         string
	client     *core.ControllerClient
	onState    func()
	onLoginURL func(string)

	mu         sync.Mutex
	cond       *sync.Cond
	buf        bytes.Buffer
	closed     bool
	ws         *websocket.Conn
	writeMu    sync.Mutex
	cancel     context.CancelFunc
	cliStart   atomic.Bool
	scanBuffer strings.Builder
}

func newWSTerminalConn(parent context.Context, client *core.ControllerClient, id string, onState func(), onLoginURL func(string)) (*wsTerminalConn, error) {
	ctx, cancel := context.WithCancel(parent)
	ws, _, err := client.DialWebSocket(ctx, "/api/terminals/"+url.PathEscape(id)+"/stream", false)
	if err != nil {
		cancel()
		return nil, err
	}
	c := &wsTerminalConn{
		id:         id,
		client:     client,
		ws:         ws,
		cancel:     cancel,
		onState:    onState,
		onLoginURL: onLoginURL,
	}
	c.cond = sync.NewCond(&c.mu)
	go c.readLoop(ctx)
	return c, nil
}

func (c *wsTerminalConn) readLoop(ctx context.Context) {
	defer c.Close()
	stopWatch := context.AfterFunc(ctx, func() {
		_ = c.Close()
	})
	defer stopWatch()

	for {
		msgType, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		if msgType == websocket.TextMessage {
			var ctrl struct {
				Type    string `json:"type"`
				Pending bool   `json:"pending"`
			}
			if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == "cli-startup" {
				c.cliStart.Store(ctrl.Pending)
				if c.onState != nil {
					c.onState()
				}
			}
			continue
		}
		if len(data) == 0 {
			continue
		}
		if found := ExtractClaudeAuthURL(&c.scanBuffer, data); found != "" && c.onLoginURL != nil {
			c.onLoginURL(found)
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		_, _ = c.buf.Write(data)
		c.cond.Broadcast()
		c.mu.Unlock()
	}
}

func (c *wsTerminalConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.buf.Len() == 0 && !c.closed {
		c.cond.Wait()
	}
	if c.buf.Len() == 0 && c.closed {
		return 0, io.EOF
	}
	return c.buf.Read(p)
}

func (c *wsTerminalConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	closed := c.closed
	ws := c.ws
	c.mu.Unlock()
	if closed || ws == nil {
		return 0, io.ErrClosedPipe
	}
	_ = ws.SetWriteDeadline(time.Now().Add(8 * time.Second))
	if err := ws.WriteJSON(map[string]string{
		"type": "input",
		"data": string(p),
	}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsTerminalConn) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	closed := c.closed
	ws := c.ws
	c.mu.Unlock()
	if closed || ws == nil {
		return io.ErrClosedPipe
	}
	_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return ws.WriteJSON(map[string]any{
		"type": "resize",
		"cols": cols,
		"rows": rows,
	})
}

func (c *wsTerminalConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	ws := c.ws
	cancel := c.cancel
	c.cond.Broadcast()
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if ws != nil {
		return ws.Close()
	}
	return nil
}

// TerminalSession owns one live Ghostty terminal emulator bound to a Cage PTY WebSocket.
type TerminalSession struct {
	id         string
	client     *core.ControllerClient
	conn       *wsTerminalConn
	term       *terminal.Terminal
	invalidate func()

	mu           sync.RWMutex
	loginURL     string
	dismissedURL string
	lastErr      string
	exited       bool
}

func OpenTerminalSession(ctx context.Context, client *core.ControllerClient, id string, font terminal.Font, dark bool, openExternal func(string), invalidate func()) (*TerminalSession, error) {
	sess := &TerminalSession{
		id:         id,
		client:     client,
		invalidate: invalidate,
	}
	conn, err := newWSTerminalConn(ctx, client, id, invalidate, func(raw string) {
		sess.mu.Lock()
		changed := raw != sess.loginURL && raw != sess.dismissedURL
		if changed {
			sess.loginURL = raw
		}
		sess.mu.Unlock()
		if changed && invalidate != nil {
			invalidate()
		}
	})
	if err != nil {
		return nil, err
	}
	sess.conn = conn

	lightTheme := terminal.LightTheme()
	darkTheme := terminal.DarkTheme()
	opts := terminal.Options{
		Conn:         conn,
		ResizeByConn: false,
		Font:         font,
		Theme:        lightTheme,
		DarkTheme:    darkTheme,
		CopyOnSelect: terminal.CopyOnSelectClipboard,
		OpenLink: func(link string) bool {
			if !IsTerminalBrowserURL(link) {
				return false
			}
			go func() {
				if err := sess.OpenInBrowser(context.Background(), link); err == nil && openExternal != nil {
					openExternal(link)
				}
			}()
			return true
		},
		OnPaste: func(d transfer.Data) bool {
			return sess.handlePaste(d)
		},
		OnDrop: func(paths []string) bool {
			return sess.handleDrop(paths)
		},
		OnExit: func(int) {
			sess.mu.Lock()
			sess.exited = true
			sess.mu.Unlock()
			if invalidate != nil {
				invalidate()
			}
		},
	}
	_ = dark
	term, err := terminal.New(opts)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	sess.term = term
	return sess, nil
}

func (s *TerminalSession) handlePaste(d transfer.Data) bool {
	if files, err := d.Files(); err == nil && len(files) > 0 {
		return s.handleDrop(files)
	}
	if _, ok := d.Preferred(transfer.PNG); ok {
		pngBytes, err := d.Read(transfer.PNG)
		if err == nil && len(pngBytes) > 0 {
			go s.uploadImageAndPaste(pngBytes)
			return true
		}
	}
	return false
}

func (s *TerminalSession) uploadImageAndPaste(pngBytes []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var res struct {
		Path string `json:"path"`
	}
	err := s.client.PostJSON(ctx, "/api/input/image", map[string]any{
		"target": "terminal",
		"id":     s.id,
		"png":    base64.StdEncoding.EncodeToString(pngBytes),
	}, &res)
	if err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		if s.invalidate != nil {
			s.invalidate()
		}
		return
	}
	if res.Path != "" {
		s.term.Paste(shellescape.Quote(res.Path) + " ")
	}
}

func (s *TerminalSession) handleDrop(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		args := append([]string{"terminal", s.id}, paths...)
		res, err := s.client.RunFileCommand(ctx, "upload-files", args...)
		if err != nil {
			s.mu.Lock()
			s.lastErr = err.Error()
			s.mu.Unlock()
			if s.invalidate != nil {
				s.invalidate()
			}
			return
		}
		var quoted []string
		if rawPaths, ok := res["paths"].([]any); ok {
			for _, item := range rawPaths {
				if p, ok := item.(string); ok && p != "" {
					quoted = append(quoted, shellescape.Quote(p))
				}
			}
		}
		if len(quoted) > 0 {
			s.term.Paste(strings.Join(quoted, " ") + " ")
		}
	}()
	return true
}

func (s *TerminalSession) CLIStarting() bool {
	if s.conn == nil {
		return false
	}
	return s.conn.cliStart.Load()
}

func (s *TerminalSession) DetectedLoginURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loginURL
}

func (s *TerminalSession) DismissLoginURL() {
	s.mu.Lock()
	s.dismissedURL = s.loginURL
	s.loginURL = ""
	s.mu.Unlock()
	if s.invalidate != nil {
		s.invalidate()
	}
}

func (s *TerminalSession) LastError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

func (s *TerminalSession) ClearError() {
	s.mu.Lock()
	s.lastErr = ""
	s.mu.Unlock()
}

func IsTerminalBrowserURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 8192 || strings.ContainsAny(raw, "\r\n\t\x00") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return (scheme == "http" || scheme == "https") && u.Host != ""
}

func (s *TerminalSession) OpenInBrowser(ctx context.Context, rawURL string) error {
	if !IsTerminalBrowserURL(rawURL) {
		return nil
	}
	if IsClaudeAuthURL(rawURL) {
		return s.OpenLoginURL(ctx, rawURL)
	}
	if err := s.client.PostJSON(ctx, "/api/browser/new", map[string]string{
		"url": rawURL,
	}, nil); err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		if s.invalidate != nil {
			s.invalidate()
		}
		return err
	}
	return nil
}

func (s *TerminalSession) OpenLoginURL(ctx context.Context, rawURL string) error {
	if rawURL == "" {
		s.mu.RLock()
		rawURL = s.loginURL
		s.mu.RUnlock()
	}
	if !IsClaudeAuthURL(rawURL) {
		return nil
	}
	if err := s.client.PostJSON(ctx, "/api/terminals/"+url.PathEscape(s.id)+"/login-link", map[string]string{
		"url": rawURL,
	}, nil); err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		if s.invalidate != nil {
			s.invalidate()
		}
		return err
	}
	s.DismissLoginURL()
	return nil
}

func (s *TerminalSession) PasteText(text string) {
	if s.term != nil && text != "" {
		s.term.Paste(text)
	}
}

func (s *TerminalSession) View(c *ui.Context) ui.Element {
	if s.term == nil {
		return ui.Box(c).Fill()
	}
	return terminal.View(c, s.term).Fill()
}

func (s *TerminalSession) SetFontSize(size float32) {
	if s.term == nil || size <= 0 {
		return
	}
	s.term.SetFont(terminal.Font{
		Family: "Menlo, SF Mono, Monaco, monospace",
		Size:   size,
	})
}

func (s *TerminalSession) Close() {
	if s.term != nil {
		_ = s.term.Close()
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

// TerminalManager caches open TerminalSession instances keyed by backend terminal ID.
type TerminalManager struct {
	client       *core.ControllerClient
	openExternal func(string)
	invalidate   func()

	mu       sync.Mutex
	sessions map[string]*TerminalSession
	fontSize float32
	dark     bool
}

func NewTerminalManager(client *core.ControllerClient, openExternal func(string), invalidate func()) *TerminalManager {
	return &TerminalManager{
		client:       client,
		openExternal: openExternal,
		invalidate:   invalidate,
		sessions:     map[string]*TerminalSession{},
		fontSize:     13,
	}
}

func (m *TerminalManager) SetStyle(fontSize int, dark bool) {
	m.mu.Lock()
	sz := float32(fontSize)
	if sz <= 0 {
		sz = 13
	}
	changed := sz != m.fontSize
	m.fontSize = sz
	m.dark = dark
	var list []*TerminalSession
	if changed {
		for _, s := range m.sessions {
			list = append(list, s)
		}
	}
	m.mu.Unlock()
	for _, s := range list {
		s.SetFontSize(sz)
	}
}

func (m *TerminalManager) Reconcile(summaries []core.TerminalSummary) {
	live := make(map[string]bool, len(summaries))
	for _, t := range summaries {
		live[t.ID] = true
	}
	m.mu.Lock()
	var toClose []*TerminalSession
	for id, s := range m.sessions {
		if !live[id] {
			toClose = append(toClose, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	for _, s := range toClose {
		s.Close()
	}
}

func (m *TerminalManager) GetOrOpen(ctx context.Context, id string) (*TerminalSession, error) {
	if id == "" {
		return nil, nil
	}
	m.mu.Lock()
	if s, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		return s, nil
	}
	font := terminal.Font{
		Family: "Menlo, SF Mono, Monaco, monospace",
		Size:   m.fontSize,
	}
	dark := m.dark
	m.mu.Unlock()

	s, err := OpenTerminalSession(ctx, m.client, id, font, dark, m.openExternal, m.invalidate)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if existing, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		s.Close()
		return existing, nil
	}
	m.sessions[id] = s
	m.mu.Unlock()
	return s, nil
}

func (m *TerminalManager) CloseAll() {
	m.mu.Lock()
	list := make([]*TerminalSession, 0, len(m.sessions))
	for id, s := range m.sessions {
		list = append(list, s)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	for _, s := range list {
		s.Close()
	}
}
