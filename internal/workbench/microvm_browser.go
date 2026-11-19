package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sylvinus/agent-vm/internal/microvm"
)

type microVMTab struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Type   string `json:"type"`
	Socket string `json:"webSocketDebuggerUrl"`
	Active bool   `json:"active"`
}

func (s *Server) startMicroVMBrowser(ctx context.Context, target string) error {
	client, err := s.microGuest.client()
	if err != nil {
		return err
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	_, err = client.StartBrowser(ctx, microvm.BrowserStartRequest{URL: target, Language: cfg.BrowserLanguage, Timezone: cfg.Timezone})
	return err
}
func (s *Server) microVMCDP(ctx context.Context, method, path string) ([]byte, error) {
	client, err := s.microGuest.client()
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return client.DialBrowserChannel(ctx, "cdp") }}
	defer tr.CloseIdleConnections()
	httpClient := &http.Client{Transport: tr, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("CDP redirect refused") }}
	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:9222"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("browser returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
func (s *Server) microVMTabCommand(ctx context.Context, id, method string, params any) error {
	raw, err := s.microVMCDP(ctx, "GET", "/json/list")
	if err != nil {
		return err
	}
	var tabs []microVMTab
	if err = json.Unmarshal(raw, &tabs); err != nil {
		return err
	}
	var target string
	for _, tab := range tabs {
		if tab.ID == id && tab.Type == "page" {
			target = tab.Socket
		}
	}
	if target == "" {
		return errors.New("browser tab no longer exists")
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "ws" || !strings.HasPrefix(u.Path, "/devtools/page/") {
		return errors.New("invalid CDP target")
	}
	client, err := s.microGuest.client()
	if err != nil {
		return err
	}
	dialer := websocket.Dialer{Proxy: nil, HandshakeTimeout: 5 * time.Second, NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return client.DialBrowserChannel(ctx, "cdp") }}
	conn, _, err := dialer.DialContext(ctx, "ws://127.0.0.1:9222"+u.RequestURI(), nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	conn.SetReadLimit(1 << 20)
	if err = conn.WriteJSON(map[string]any{"id": 1, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		var reply struct {
			ID    int `json:"id"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err = conn.ReadJSON(&reply); err != nil {
			return err
		}
		if reply.ID == 1 {
			if reply.Error != nil {
				return errors.New(reply.Error.Message)
			}
			return nil
		}
	}
}
func (s *Server) microVMBrowserAPI(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	client, err := s.microGuest.client()
	if err != nil {
		return nil, err
	}
	if path == "/health" {
		_, err := client.Health(ctx)
		return []byte(`{"ok":true}`), err
	}
	if path == "/homepage" {
		return []byte(`{"ok":true}`), nil
	}
	status, err := client.BrowserStatus(ctx)
	if err != nil {
		return nil, err
	}
	if path == "/tabs" && !status.Running {
		return []byte(`{"ready":true,"available":true,"tabs":[]}`), nil
	}
	var input browserInput
	if len(body) > 0 {
		if err = json.Unmarshal(body, &input); err != nil {
			return nil, err
		}
	}
	if !status.Running {
		target := "about:blank"
		if path == "/new" && input.URL != "" {
			target = input.URL
		}
		if err = s.startMicroVMBrowser(ctx, target); err != nil {
			return nil, err
		}
		if path == "/new" {
			return s.microVMBrowserAPI(ctx, "GET", "/tabs", nil)
		}
	}
	switch path {
	case "/tabs":
		raw, err := s.microVMCDP(ctx, "GET", "/json/list")
		if err != nil {
			return nil, err
		}
		var tabs []microVMTab
		if err = json.Unmarshal(raw, &tabs); err != nil {
			return nil, err
		}
		pages := []microVMTab{}
		for _, tab := range tabs {
			if tab.Type == "page" {
				tab.Socket = ""
				tab.Active = len(pages) == 0
				pages = append(pages, tab)
			}
		}
		active := ""
		if len(pages) > 0 {
			active = pages[0].ID
		}
		return json.Marshal(map[string]any{"ready": true, "available": true, "tabs": pages, "active_id": active})
	case "/new":
		if input.URL == "" {
			input.URL = "about:blank"
		}
		_, err = s.microVMCDP(ctx, "PUT", "/json/new?"+url.QueryEscape(input.URL))
	case "/activate", "/close":
		_, err = s.microVMCDP(ctx, "GET", "/json"+path+"/"+url.PathEscape(input.ID))
	case "/navigate":
		err = s.microVMTabCommand(ctx, input.ID, "Page.navigate", map[string]any{"url": input.URL})
	case "/reload":
		err = s.microVMTabCommand(ctx, input.ID, "Page.reload", map[string]any{})
	case "/back", "/forward":
		expression := "history.back()"
		if path == "/forward" {
			expression = "history.forward()"
		}
		err = s.microVMTabCommand(ctx, input.ID, "Runtime.evaluate", map[string]any{"expression": expression})
	case "/insert-text":
		err = s.microVMTabCommand(ctx, input.ID, "Input.insertText", map[string]any{"text": input.Text})
	default:
		return nil, errors.New("此浏览器操作尚未接入 MicroVM 开发环境")
	}
	if err != nil {
		return nil, err
	}
	return []byte(`{"ok":true}`), nil
}
func (s *Server) microVMVNC(w http.ResponseWriter, r *http.Request) {
	if err := s.startMicroVMBrowser(r.Context(), "about:blank"); err != nil {
		requestError(w, err)
		return
	}
	client, err := s.microGuest.client()
	if err != nil {
		requestError(w, err)
		return
	}
	conn, err := client.DialBrowserChannel(r.Context(), "vnc")
	if err != nil {
		requestError(w, err)
		return
	}
	defer conn.Close()
	s.streamVNC(w, r, conn, conn, func() { _ = conn.Close() })
}
func (s *Server) subscribeMicroVMBrowser(parent context.Context) io.ReadCloser {
	ctx, cancel := context.WithCancel(parent)
	reader, writer := io.Pipe()
	stop := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()) })
	go func() {
		defer writer.Close()
		defer cancel()
		defer stop()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		previous := ""
		for {
			data, err := s.microVMBrowserAPI(ctx, "GET", "/tabs", nil)
			if err != nil {
				_ = writer.CloseWithError(err)
				return
			}
			if string(data) != previous {
				if _, err = fmt.Fprintf(writer, "data: %s\n\n", data); err != nil {
					return
				}
				previous = string(data)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return &windowsBrowserEventStream{ReadCloser: reader, cancel: cancel}
}
