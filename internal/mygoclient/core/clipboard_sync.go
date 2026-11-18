package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sylvinus/agent-vm/internal/macnative"
)

type clipboardHello struct {
	Version     int      `json:"version"`
	OS          string   `json:"os"`
	Session     string   `json:"session"`
	Formats     []string `json:"formats"`
	Available   bool     `json:"available"`
	FileRelease bool     `json:"file_release,omitempty"`
}

type clipboardPayload struct {
	Hello   *clipboardHello `json:"hello,omitempty"`
	Text    *string         `json:"text,omitempty"`
	PNG     string          `json:"png,omitempty"`
	Files   json.RawMessage `json:"files,omitempty"`
	Error   string          `json:"error,omitempty"`
	Source  string          `json:"source,omitempty"`
	Serial  int             `json:"serial,omitempty"`
	Ack     int             `json:"ack,omitempty"`
	Release []string        `json:"release,omitempty"`
}

const maxClipboardPNGBytes = 20 << 20

// ClipboardSync bridges macOS NSPasteboard with the guest /api/clipboard WebSocket
// and backs native clipboard operations for terminals, macOS guest views, and VNC.
type ClipboardSync struct {
	client       *ControllerClient
	cacheTracker *macnative.ClipboardCacheTracker

	mu           sync.Mutex
	active       bool
	cancel       context.CancelFunc
	ws           *websocket.Conn
	wsMu         sync.Mutex
	generation   uint64
	lastCount    int
	serial       int
	guestReady   bool
	guestFormats []string
}

func NewClipboardSync(client *ControllerClient) *ClipboardSync {
	tracker := macnative.NewClipboardCacheTracker()
	tracker.Start()
	return &ClipboardSync{
		client:       client,
		cacheTracker: tracker,
		lastCount:    macnative.PasteboardChangeCount(),
	}
}

func (s *ClipboardSync) CacheTracker() *macnative.ClipboardCacheTracker {
	return s.cacheTracker
}

func (s *ClipboardSync) EnsureRunning(parent context.Context, shouldRun bool) {
	s.mu.Lock()
	if shouldRun == s.active {
		s.mu.Unlock()
		return
	}
	if !shouldRun {
		cancel := s.cancel
		s.cancel = nil
		s.active = false
		s.guestReady = false
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.active = true
	s.lastCount = macnative.PasteboardChangeCount()
	s.mu.Unlock()

	go s.loop(ctx)
}

func (s *ClipboardSync) Stop() {
	s.EnsureRunning(context.Background(), false)
	s.cacheTracker.Stop()
}

func (s *ClipboardSync) CancelSyncGeneration() {
	s.mu.Lock()
	s.generation++
	s.mu.Unlock()
}

func (s *ClipboardSync) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		ws, _, err := s.client.DialWebSocket(ctx, "/api/clipboard", false)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		s.mu.Lock()
		s.ws = ws
		s.guestReady = false
		s.lastCount = macnative.PasteboardChangeCount()
		s.mu.Unlock()

		stopWatch := context.AfterFunc(ctx, func() {
			_ = ws.Close()
		})
		connCtx, connCancel := context.WithCancel(ctx)
		go s.pollHostPasteboard(connCtx, ws)

		for {
			var payload clipboardPayload
			if err := ws.ReadJSON(&payload); err != nil {
				break
			}
			s.handleIncoming(connCtx, ws, payload)
		}
		connCancel()
		stopWatch()
		_ = ws.Close()

		s.mu.Lock()
		if s.ws == ws {
			s.ws = nil
			s.guestReady = false
		}
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

func (s *ClipboardSync) sendPayload(ws *websocket.Conn, payload clipboardPayload) error {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	_ = ws.SetWriteDeadline(time.Now().Add(8 * time.Second))
	return ws.WriteJSON(payload)
}

func (s *ClipboardSync) handleIncoming(ctx context.Context, ws *websocket.Conn, p clipboardPayload) {
	if p.Hello != nil {
		s.mu.Lock()
		s.guestReady = p.Hello.Available
		s.guestFormats = append([]string(nil), p.Hello.Formats...)
		s.lastCount = macnative.PasteboardChangeCount()
		s.mu.Unlock()
	}
	if p.Text != nil {
		if macnative.PasteboardWriteText(*p.Text) {
			s.mu.Lock()
			s.lastCount = macnative.PasteboardChangeCount()
			s.mu.Unlock()
		}
		if p.Serial > 0 {
			_ = s.sendPayload(ws, clipboardPayload{Ack: p.Serial})
		}
		return
	}
	if p.PNG != "" {
		if raw, err := base64.StdEncoding.DecodeString(p.PNG); err == nil && len(raw) > 0 {
			if macnative.PasteboardWritePNG(raw) == nil {
				s.mu.Lock()
				s.lastCount = macnative.PasteboardChangeCount()
				s.mu.Unlock()
			}
		}
		if p.Serial > 0 {
			_ = s.sendPayload(ws, clipboardPayload{Ack: p.Serial})
		}
		return
	}
	if len(p.Files) > 0 {
		var cacheID string
		if json.Unmarshal(p.Files, &cacheID) == nil && cacheID != "" {
			s.mu.Lock()
			gen := s.generation
			s.mu.Unlock()
			go func(serial int) {
				_, _ = s.ReceiveGuestFilesToPasteboard(ctx, cacheID, gen)
				if serial > 0 {
					_ = s.sendPayload(ws, clipboardPayload{Ack: serial})
				}
			}(p.Serial)
		}
	}
}

func (s *ClipboardSync) pollHostPasteboard(ctx context.Context, ws *websocket.Conn) {
	ticker := time.NewTicker(350 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			ready := s.guestReady
			last := s.lastCount
			s.mu.Unlock()
			if !ready {
				continue
			}
			cur := macnative.PasteboardChangeCount()
			if cur == last {
				continue
			}
			s.mu.Lock()
			s.lastCount = cur
			s.serial++
			serial := s.serial
			s.mu.Unlock()

			if files := macnative.PasteboardReadFileURLs(); len(files) > 0 {
				if id, err := s.SendHostFilesToGuest(ctx, files); err == nil && id != "" {
					rawID, _ := json.Marshal(id)
					_ = s.sendPayload(ws, clipboardPayload{Files: rawID, Serial: serial})
					continue
				}
			}
			if png, ok, err := macnative.PasteboardReadPNG(); err == nil && ok && len(png) > 0 && len(png) <= maxClipboardPNGBytes {
				b64 := base64.StdEncoding.EncodeToString(png)
				_ = s.sendPayload(ws, clipboardPayload{PNG: b64, Serial: serial})
				continue
			}
			if txt := macnative.PasteboardReadText(); txt != "" {
				_ = s.sendPayload(ws, clipboardPayload{Text: &txt, Serial: serial})
			}
		}
	}
}

// ReadContent returns structured clipboard content ("text" | "image" | "files" | "empty") matching Cage.swift.
func (s *ClipboardSync) ReadContent(ctx context.Context, expectedRevision *int) map[string]any {
	cur := macnative.PasteboardChangeCount()
	if expectedRevision != nil && *expectedRevision != cur {
		return map[string]any{"kind": "changed", "revision": cur}
	}
	if urls := macnative.PasteboardReadFileURLs(); len(urls) > 0 {
		s.mu.Lock()
		gen := s.generation
		s.mu.Unlock()
		res, err := s.SendHostFilesToGuestResult(ctx, urls, gen)
		if err != nil {
			return map[string]any{"kind": "error", "error": err.Error(), "revision": cur}
		}
		res["revision"] = cur
		return res
	}
	if png, ok, err := macnative.PasteboardReadPNG(); err == nil && ok && len(png) > 0 {
		if len(png) > maxClipboardPNGBytes {
			return map[string]any{"kind": "error", "error": "剪贴板图片超过 20 MB，无法粘贴到虚拟机", "revision": cur}
		}
		return map[string]any{
			"kind":     "image",
			"png":      base64.StdEncoding.EncodeToString(png),
			"revision": cur,
		}
	}
	if txt := macnative.PasteboardReadText(); txt != "" {
		return map[string]any{
			"kind":     "text",
			"text":     txt,
			"revision": cur,
		}
	}
	return map[string]any{"kind": "empty", "revision": cur}
}

func (s *ClipboardSync) WriteContent(ctx context.Context, payload map[string]any) error {
	if text, ok := payload["text"].(string); ok {
		if !macnative.PasteboardWriteText(text) {
			return errors.New("无法写入本机剪贴板文字")
		}
		s.mu.Lock()
		s.lastCount = macnative.PasteboardChangeCount()
		s.mu.Unlock()
		return nil
	}
	if b64, ok := payload["png"].(string); ok && b64 != "" {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return err
		}
		if err := macnative.PasteboardWritePNG(raw); err != nil {
			return err
		}
		s.mu.Lock()
		s.lastCount = macnative.PasteboardChangeCount()
		s.mu.Unlock()
		return nil
	}
	if id, ok := payload["files"].(string); ok && id != "" {
		s.mu.Lock()
		gen := s.generation
		s.mu.Unlock()
		_, err := s.ReceiveGuestFilesToPasteboard(ctx, id, gen)
		return err
	}
	return errors.New("剪贴板内容为空")
}

func (s *ClipboardSync) SendHostFilesToGuest(ctx context.Context, paths []string) (string, error) {
	args := append([]string{"send"}, paths...)
	res, err := s.client.RunFileCommand(ctx, "clipboard-files", args...)
	if err != nil {
		return "", err
	}
	id, _ := res["id"].(string)
	if id == "" {
		id, _ = res["files"].(string)
	}
	if id == "" {
		return "", errors.New("未能创建剪贴板文件会话")
	}
	return id, nil
}

func (s *ClipboardSync) SendHostFilesToGuestResult(ctx context.Context, paths []string, gen uint64) (map[string]any, error) {
	id, err := s.SendHostFilesToGuest(ctx, paths)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	stale := s.generation != gen
	s.mu.Unlock()
	if stale {
		if id != "" {
			_, _ = s.client.RunFileCommand(ctx, "clipboard-files", "release", id)
		}
		return map[string]any{"kind": "canceled"}, nil
	}
	return map[string]any{"kind": "files", "id": id, "files": id}, nil
}

func (s *ClipboardSync) ReceiveGuestFilesToPasteboard(ctx context.Context, id string, gen uint64) ([]string, error) {
	if id == "" {
		return nil, errors.New("剪贴板文件标识无效")
	}
	s.cacheTracker.BeginPending(id, nil)
	defer s.cacheTracker.EndPending(id)

	res, err := s.client.RunFileCommand(ctx, "clipboard-files", "receive", id)
	if err != nil {
		return nil, err
	}
	var paths []string
	if rawList, ok := res["paths"].([]any); ok {
		for _, item := range rawList {
			if p, ok := item.(string); ok && p != "" {
				paths = append(paths, p)
			}
		}
	}
	if cacheID, _ := res["cache_id"].(string); cacheID != "" && len(paths) > 0 {
		s.cacheTracker.BeginPending(cacheID, paths)
		defer s.cacheTracker.EndPending(cacheID)
		s.cacheTracker.Publish(cacheID)
	}
	s.mu.Lock()
	stale := s.generation != gen
	s.mu.Unlock()
	if stale {
		return nil, errors.New("剪贴板同步已取消")
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("未收到可写入剪贴板的文件 (%s)", id)
	}
	if !macnative.PasteboardWriteFileURLs(paths) {
		return nil, errors.New("无法写入本机剪贴板文件")
	}
	s.mu.Lock()
	s.lastCount = macnative.PasteboardChangeCount()
	s.mu.Unlock()
	return paths, nil
}
