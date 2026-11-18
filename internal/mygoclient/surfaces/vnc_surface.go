package surfaces

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/macnative"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

type VNCState struct {
	Connection string
	Status     string
	LastError  string
	SizeError  bool
	Fallback   string
	FallbackHi string
}

// VNCBridge is bound into MyGo as "VNCBridge" so web-surface/vnc-surface.js calls Go directly
// via window.mygo.call("VNCBridge.<Method>", ...).
type VNCBridge struct {
	client     *core.ControllerClient
	clipboard  *core.ClipboardSync
	selections *macnative.FileSelectionStore
	invalidate func()

	mu     sync.RWMutex
	states map[string]VNCState
}

func NewVNCBridge(client *core.ControllerClient, clip *core.ClipboardSync, invalidate func()) *VNCBridge {
	return &VNCBridge{
		client:     client,
		clipboard:  clip,
		selections: macnative.NewFileSelectionStore(),
		invalidate: invalidate,
		states: map[string]VNCState{
			"browser": {Connection: "disconnected"},
			"desktop": {Connection: "disconnected"},
		},
	}
}

func (b *VNCBridge) SnapshotState(mode string) VNCState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.states[mode]
}

func (b *VNCBridge) ReportStatus(mode, text string) {
	b.mu.Lock()
	st := b.states[mode]
	st.Status = text
	b.states[mode] = st
	b.mu.Unlock()
	if b.invalidate != nil {
		b.invalidate()
	}
}

func (b *VNCBridge) ReportConnection(mode, status string, sizeError bool) {
	b.mu.Lock()
	st := b.states[mode]
	st.Connection = status
	st.SizeError = sizeError
	if status == "connected" {
		st.LastError = ""
	}
	b.states[mode] = st
	b.mu.Unlock()
	if b.invalidate != nil {
		b.invalidate()
	}
}

func (b *VNCBridge) ReportError(mode, message string) {
	b.mu.Lock()
	st := b.states[mode]
	st.LastError = strings.TrimSpace(message)
	b.states[mode] = st
	b.mu.Unlock()
	if b.invalidate != nil {
		b.invalidate()
	}
}

func (b *VNCBridge) ReportFallback(mode, text, hint string) {
	b.mu.Lock()
	st := b.states[mode]
	st.Fallback = text
	st.FallbackHi = hint
	b.states[mode] = st
	b.mu.Unlock()
	if b.invalidate != nil {
		b.invalidate()
	}
}

// RefreshSessionTicket issues a fresh one-time session ticket so WKWebView can
// authenticate /api/session without relying on WKHTTPCookieStore.
func (b *VNCBridge) RefreshSessionTicket() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return b.client.IssueLaunchTicket(ctx)
}

func (b *VNCBridge) ClipboardRevision() int {
	return macnative.PasteboardChangeCount()
}

func (b *VNCBridge) ClipboardReadContent() map[string]any {
	if b.clipboard == nil {
		return map[string]any{"kind": "empty"}
	}
	return b.clipboard.ReadContent(context.Background(), nil)
}

func (b *VNCBridge) ClipboardSyncWrite(_ string, content map[string]any) (map[string]any, error) {
	if b.clipboard == nil {
		return nil, errors.New("剪贴板同步未启动")
	}
	if revFloat, ok := content["revision"].(float64); ok {
		if int(revFloat) != macnative.PasteboardChangeCount() {
			return map[string]any{"superseded": true}, nil
		}
	}
	if err := b.clipboard.WriteContent(context.Background(), content); err != nil {
		return nil, err
	}
	return map[string]any{"superseded": false, "ok": true}, nil
}

func (b *VNCBridge) SyncGuestClipboard(_ string) (bool, error) {
	return true, nil
}

func (b *VNCBridge) UploadSelectedFiles(target, id, selectionID string) (map[string]any, error) {
	paths, ok := b.selections.Take(selectionID)
	if !ok || len(paths) == 0 {
		return nil, errors.New("文件选择已过期，请重新选择")
	}
	args := append([]string{target, id}, paths...)
	return b.client.RunFileCommand(context.Background(), "upload-files", args...)
}

type vncViewConfig struct {
	Origin          string
	Mode            string
	TabID           string
	ConnectionID    string
	Keyboard        string
	GuestPlatform   string
	SharedClipboard bool
}

type managedVNCView struct {
	wv          *mygo.WebView
	appliedCfg  vncViewConfig
	desiredCfg  vncViewConfig
	creating    bool
	configuring bool
	createErr   string
	generation  uint64
}

// VNCSurfaceManager asynchronously manages WKWebView instances hosting /vnc-surface.html
// for Linux VM and Cloud SSH Browser / Claude Desktop VNC canvases without blocking ui.View.
type VNCSurfaceManager struct {
	client     *core.ControllerClient
	invalidate func()

	mu     sync.Mutex
	win    *mygo.Window
	closed bool
	views  map[string]*managedVNCView
}

func NewVNCSurfaceManager(client *core.ControllerClient, invalidate func()) *VNCSurfaceManager {
	return &VNCSurfaceManager{
		client:     client,
		invalidate: invalidate,
		views:      map[string]*managedVNCView{},
	}
}

func (m *VNCSurfaceManager) SetWindow(win *mygo.Window) {
	m.mu.Lock()
	m.win = win
	m.mu.Unlock()
}

// SyncSurface is non-blocking and safe to call from ui.View. It returns the active WebView
// when ready (or nil while initializing asynchronously) and any creation error.
func (m *VNCSurfaceManager) SyncSurface(mode, tabID, connectionID, keyboard, guestPlatform string, sharedClipboard bool) (*mygo.WebView, string) {
	if mode != "desktop" {
		mode = "browser"
	}
	if keyboard == "" {
		keyboard = "mac"
	}
	if guestPlatform == "" {
		guestPlatform = "linux"
	}
	if connectionID == "" {
		connectionID = "local"
	}
	origin := m.client.Origin()
	if origin == "" {
		return nil, ""
	}

	next := vncViewConfig{
		Origin:          origin,
		Mode:            mode,
		TabID:           tabID,
		ConnectionID:    connectionID,
		Keyboard:        keyboard,
		GuestPlatform:   guestPlatform,
		SharedClipboard: sharedClipboard,
	}

	m.mu.Lock()
	if m.closed || m.win == nil {
		m.mu.Unlock()
		return nil, ""
	}
	slot := m.views[mode]
	if slot == nil {
		slot = &managedVNCView{}
		m.views[mode] = slot
	}
	slot.desiredCfg = next

	if slot.wv != nil && !slot.wv.IsDestroyed() && slot.appliedCfg.Origin == next.Origin {
		wv := slot.wv
		if slot.desiredCfg != slot.appliedCfg && !slot.configuring {
			slot.configuring = true
			go m.pushConfigAsync(mode)
		}
		m.mu.Unlock()
		return wv, ""
	}

	if !slot.creating && slot.createErr == "" {
		slot.creating = true
		slot.generation++
		gen := slot.generation
		m.mu.Unlock()
		go m.createSurfaceAsync(mode, gen)
		return nil, ""
	}
	createErr := slot.createErr
	m.mu.Unlock()
	return nil, createErr
}

func (m *VNCSurfaceManager) createSurfaceAsync(mode string, gen uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	ticket, _ := m.client.IssueLaunchTicket(ctx)
	cancel()

	m.mu.Lock()
	slot := m.views[mode]
	win := m.win
	if m.closed || win == nil || slot == nil || slot.generation != gen {
		if slot != nil && slot.generation == gen {
			slot.creating = false
		}
		m.mu.Unlock()
		return
	}
	if latestOrigin := m.client.Origin(); latestOrigin != "" {
		slot.desiredCfg.Origin = latestOrigin
	}
	cfg := slot.desiredCfg
	m.mu.Unlock()

	q := url.Values{
		"mode":            {cfg.Mode},
		"tab":             {cfg.TabID},
		"connection_id":   {cfg.ConnectionID},
		"keyboard":        {cfg.Keyboard},
		"guestPlatform":   {cfg.GuestPlatform},
		"sharedClipboard": {boolString(cfg.SharedClipboard)},
		"systemClipboard": {"true"},
	}
	targetURL := cfg.Origin + "/vnc-surface.html?" + q.Encode()
	if ticket != "" {
		targetURL += "#" + url.QueryEscape(ticket)
	}

	wv, err := win.NewWebView(mygo.WebViewOptions{
		URL:             targetURL,
		BackgroundColor: "#ffffff",
		Page: mygo.PageOptions{
			TrustedOrigins: []string{cfg.Origin},
		},
	})

	var oldWV *mygo.WebView
	m.mu.Lock()
	slot = m.views[mode]
	if m.closed || slot == nil || slot.generation != gen {
		if slot != nil && slot.generation == gen {
			slot.creating = false
		}
		m.mu.Unlock()
		if wv != nil && !wv.IsDestroyed() {
			wv.Destroy()
		}
		return
	}
	if slot.desiredCfg.Origin != cfg.Origin {
		slot.generation++
		nextGen := slot.generation
		m.mu.Unlock()
		if wv != nil && !wv.IsDestroyed() {
			wv.Destroy()
		}
		go m.createSurfaceAsync(mode, nextGen)
		return
	}
	slot.creating = false
	if err != nil {
		slot.createErr = err.Error()
		m.mu.Unlock()
		if m.invalidate != nil {
			m.invalidate()
		}
		return
	}
	oldWV = slot.wv
	slot.wv = wv
	slot.appliedCfg = cfg
	slot.createErr = ""
	needPush := slot.desiredCfg != slot.appliedCfg && !slot.configuring
	if needPush {
		slot.configuring = true
	}
	m.mu.Unlock()

	if oldWV != nil && !oldWV.IsDestroyed() {
		oldWV.Destroy()
	}
	if needPush {
		go m.pushConfigAsync(mode)
	}
	if m.invalidate != nil {
		m.invalidate()
	}
}

func (m *VNCSurfaceManager) pushConfigAsync(mode string) {
	for {
		m.mu.Lock()
		slot := m.views[mode]
		if m.closed || slot == nil || slot.wv == nil || slot.wv.IsDestroyed() {
			if slot != nil {
				slot.configuring = false
			}
			m.mu.Unlock()
			return
		}
		next := slot.desiredCfg
		if next == slot.appliedCfg {
			slot.configuring = false
			m.mu.Unlock()
			return
		}
		wv := slot.wv
		m.mu.Unlock()

		cfgJSON, _ := json.Marshal(map[string]any{
			"mode":            next.Mode,
			"tabId":           next.TabID,
			"connectionId":    next.ConnectionID,
			"keyboard":        next.Keyboard,
			"guestPlatform":   next.GuestPlatform,
			"sharedClipboard": next.SharedClipboard,
		})
		_, _ = wv.Page().Eval("window.cageVNC && window.cageVNC.configure(" + string(cfgJSON) + ")")

		m.mu.Lock()
		if slot = m.views[mode]; slot != nil && slot.wv == wv {
			slot.appliedCfg = next
		}
		m.mu.Unlock()
	}
}

func (m *VNCSurfaceManager) View(c *ui.Context, wv *mygo.WebView) ui.Element {
	if wv == nil || wv.IsDestroyed() {
		return ui.Column(c).Fill().Center().Gap(10).Children(func() {
			ui.Spinner(c)
			ui.Text(c, "正在连接远程桌面画布…").FontSize(12)
		})
	}
	return ui.WebView(c, wv).Fill().Grow(1)
}

func (m *VNCSurfaceManager) Reconnect(mode string) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	slot := m.views[mode]
	if slot == nil {
		m.mu.Unlock()
		return
	}
	if slot.wv == nil || slot.wv.IsDestroyed() {
		slot.createErr = ""
		if !slot.creating && slot.desiredCfg.Origin != "" {
			slot.creating = true
			slot.generation++
			gen := slot.generation
			m.mu.Unlock()
			go m.createSurfaceAsync(mode, gen)
			return
		}
		m.mu.Unlock()
		return
	}
	wv := slot.wv
	m.mu.Unlock()
	go func() {
		_, _ = wv.Page().Eval("window.cageVNC && window.cageVNC.reconnect()")
	}()
}

func (m *VNCSurfaceManager) Edit(mode, action string) {
	m.mu.Lock()
	slot := m.views[mode]
	var wv *mygo.WebView
	if slot != nil {
		wv = slot.wv
	}
	m.mu.Unlock()
	if wv != nil && !wv.IsDestroyed() {
		arg, _ := json.Marshal(action)
		go func() {
			_, _ = wv.Page().Eval("window.cageVNC && window.cageVNC.edit(" + string(arg) + ")")
		}()
	}
}

func (m *VNCSurfaceManager) CloseAll() {
	m.mu.Lock()
	m.closed = true
	list := make([]*managedVNCView, 0, len(m.views))
	for k, v := range m.views {
		list = append(list, v)
		delete(m.views, k)
	}
	m.mu.Unlock()
	for _, entry := range list {
		if entry.wv != nil && !entry.wv.IsDestroyed() {
			entry.wv.Destroy()
		}
	}
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
