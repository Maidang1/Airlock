package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/macnative"
)

// NativeDialogOptions mirrors macOS open/save sheet options cleanly without importing mygo UI into tests.
type NativeDialogOptions struct {
	Title       string
	Message     string
	DefaultPath string
	ButtonLabel string
	Directory   bool
	Multiple    bool
}

// HostBridge abstracts desktop-window, dialog, shell, and app-updater operations so
// AgentNativeLoop runs cleanly both in the full MyGo app and in headless unit tests.
type HostBridge interface {
	OpenDialog(opts NativeDialogOptions) ([]string, error)
	SaveDialog(opts NativeDialogOptions) (string, error)
	OpenExternal(url string) error
	OpenPath(path string) error
	ShowItemInFolder(path string) error
	ShowAndFocusWindow()
	MarkWorkbenchReady()
	HandleUpdate(operation string) map[string]any
}

type agentNativeItem struct {
	ID         string          `json:"id"`
	Connection string          `json:"connection_id,omitempty"`
	Request    json.RawMessage `json:"request"`
	Status     string          `json:"status,omitempty"`
}

// AccountDeviceService shares Keychain/Touch ID device enrollment and unlock across UI views and remote native actions.
type AccountDeviceService struct {
	client *ControllerClient
}

func NewAccountDeviceService(client *ControllerClient) *AccountDeviceService {
	return &AccountDeviceService{client: client}
}

func (a *AccountDeviceService) Status(binding string, devices map[string]string) map[string]any {
	return macnative.AccountDeviceStatus(binding, devices)
}

func (a *AccountDeviceService) Enroll(ctx context.Context, kind, binding string) (map[string]any, error) {
	if binding == "" {
		return nil, errors.New("缺少账户设备绑定标识")
	}
	var cred struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := a.client.PostJSON(ctx, "/api/account/device-enroll", map[string]string{"kind": kind}, &cred); err != nil {
		return nil, err
	}
	if err := macnative.StoreAccountDevice(kind, binding, cred.ID, cred.Secret); err != nil {
		// Keep Keychain and server record in sync on entitlement/permission errors.
		_ = a.client.PostJSON(ctx, "/api/account/device-revoke", map[string]string{"kind": kind}, nil)
		return nil, err
	}
	var view AccountView
	_ = a.client.GetJSON(ctx, "/api/account", &view)
	return a.Status(view.DeviceBinding, view.Devices), nil
}

func (a *AccountDeviceService) Revoke(ctx context.Context, kind, binding string) (map[string]any, error) {
	_ = macnative.RemoveAccountDevice(kind, binding)
	var view AccountView
	if err := a.client.PostJSON(ctx, "/api/account/device-revoke", map[string]string{"kind": kind}, &view); err != nil {
		return nil, err
	}
	return a.Status(view.DeviceBinding, view.Devices), nil
}

func (a *AccountDeviceService) Unlock(ctx context.Context, kind, binding, reason string) (AccountView, error) {
	if binding == "" {
		return AccountView{}, errors.New("缺少账户设备绑定标识")
	}
	id, secret, err := macnative.ReadAccountDevice(kind, binding, reason)
	if err != nil {
		return AccountView{}, err
	}
	var view AccountView
	body := map[string]any{
		"kind": kind,
		"credential": map[string]string{
			"id":     id,
			"secret": secret,
		},
	}
	if err := a.client.PostJSON(ctx, "/api/account/device-unlock", body, &view); err != nil {
		return AccountView{}, err
	}
	return view, nil
}

// AgentNativeLoop consumes pending /api/agent/native window actions from `cage agent`.
type AgentNativeLoop struct {
	client     *ControllerClient
	store      *Store
	clipboard  *ClipboardSync
	selections *macnative.FileSelectionStore
	bridge     HostBridge
	devices    *AccountDeviceService

	mu     sync.Mutex
	cancel context.CancelFunc
}

func NewAgentNativeLoop(client *ControllerClient, store *Store, clip *ClipboardSync, bridge HostBridge) *AgentNativeLoop {
	return &AgentNativeLoop{
		client:     client,
		store:      store,
		clipboard:  clip,
		selections: macnative.NewFileSelectionStore(),
		bridge:     bridge,
		devices:    NewAccountDeviceService(client),
	}
}

func (l *AgentNativeLoop) Start(parent context.Context) {
	l.mu.Lock()
	if l.cancel != nil {
		l.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	l.cancel = cancel
	l.mu.Unlock()

	go l.loop(ctx)
}

func (l *AgentNativeLoop) Stop() {
	l.mu.Lock()
	cancel := l.cancel
	l.cancel = nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (l *AgentNativeLoop) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		proof := l.client.WindowProof()
		if proof == "" {
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
				continue
			}
		}
		if err := l.PollOnce(ctx); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(1500 * time.Millisecond):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (l *AgentNativeLoop) PollOnce(ctx context.Context) error {
	proof := l.client.WindowProof()
	if proof == "" {
		return errors.New("缺少窗口访问凭证")
	}
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := l.client.prepareRequest(reqCtx, http.MethodGet, "/api/agent/native?consumer=window", nil, "")
	if err != nil {
		return err
	}
	req.Header.Set("X-Cage-Agent-Window", proof)
	if req.Header.Get(connectionHeader) == "" {
		req.Header.Set(connectionHeader, "local")
	}
	resp, err := l.client.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_ = l.client.reauthenticate(ctx)
		return fmt.Errorf("agent native poll HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("agent native poll HTTP %d", resp.StatusCode)
	}

	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil
	}
	var items []agentNativeItem
	var single agentNativeItem
	if err := json.Unmarshal(raw, &single); err == nil && single.ID != "" {
		items = []agentNativeItem{single}
	} else if err := json.Unmarshal(raw, &items); err != nil {
		var envelope struct {
			Items []agentNativeItem `json:"items"`
		}
		if err := json.Unmarshal(raw, &envelope); err == nil {
			items = envelope.Items
		}
	}
	for _, item := range items {
		if item.ID == "" {
			continue
		}
		l.handleAndComplete(ctx, proof, item)
	}
	return nil
}

func (l *AgentNativeLoop) handleAndComplete(ctx context.Context, proof string, item agentNativeItem) {
	var reqMap map[string]any
	if err := json.Unmarshal(item.Request, &reqMap); err != nil {
		_ = l.completeItem(ctx, proof, item.ID, nil, errors.New("原生请求格式无效"))
		return
	}
	result, err := l.ExecuteRequest(ctx, reqMap)
	_ = l.completeItem(ctx, proof, item.ID, result, err)
}

func (l *AgentNativeLoop) completeItem(ctx context.Context, proof, id string, result any, execErr error) error {
	payload := map[string]any{}
	if execErr != nil {
		payload["error"] = execErr.Error()
	} else {
		payload["result"] = result
	}
	data, _ := json.Marshal(payload)
	req, err := l.client.prepareRequest(ctx, http.MethodPost, "/api/agent/native/"+id+"/complete", data, "application/json")
	if err != nil {
		return err
	}
	req.Header.Set("X-Cage-Agent-Window", proof)
	if req.Header.Get(connectionHeader) == "" {
		req.Header.Set(connectionHeader, "local")
	}
	resp, err := l.client.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("complete HTTP %d", resp.StatusCode)
	}
	return nil
}

// ExecuteRequest handles one native bridge command map (also callable directly by tests or internal bridges).
func (l *AgentNativeLoop) ExecuteRequest(ctx context.Context, req map[string]any) (any, error) {
	action, _ := req["action"].(string)
	switch action {
	case "accountDevice":
		return l.handleAccountDevice(ctx, req)
	case "clipboardRead":
		return macnative.PasteboardReadText(), nil
	case "clipboardRevision":
		return macnative.PasteboardChangeCount(), nil
	case "clipboardSyncCancel":
		if l.clipboard != nil {
			l.clipboard.CancelSyncGeneration()
		}
		return true, nil
	case "clipboardSyncRelease":
		id, _ := req["id"].(string)
		if id == "" {
			return false, errors.New("缺少剪贴板文件标识")
		}
		return l.client.RunFileCommand(ctx, "clipboard-files", "release", id)
	case "clipboardSyncRead", "clipboardReadContent":
		var exp *int
		if v, ok := asInt(req["expected_revision"]); ok {
			exp = &v
		}
		return l.clipboard.ReadContent(ctx, exp), nil
	case "clipboardSyncWrite":
		if err := l.clipboard.WriteContent(ctx, req); err != nil {
			return nil, err
		}
		return true, nil
	case "clipboardWrite":
		if text, ok := req["text"].(string); ok {
			if concealed, _ := req["concealed"].(bool); concealed {
				ttl := 60
				if v, ok := asInt(req["clear_seconds"]); ok && v > 0 {
					ttl = v
				}
				return macnative.PasteboardWriteConcealedText(text, ttl), nil
			}
			return macnative.PasteboardWriteText(text), nil
		}
		if pngB64, ok := req["png"].(string); ok && pngB64 != "" {
			raw, err := base64.StdEncoding.DecodeString(pngB64)
			if err != nil {
				return nil, err
			}
			return true, macnative.PasteboardWritePNG(raw)
		}
		return false, errors.New("剪贴板写入内容为空")
	case "selectFile", "selectFiles", "openFile", "selectDirectory", "openDirectory", "browseWorkspace":
		if l.bridge == nil {
			return nil, errors.New("当前界面不支持文件选择器")
		}
		isDir := action == "selectDirectory" || action == "openDirectory" || action == "browseWorkspace"
		multi, _ := req["multiple"].(bool)
		if action == "selectFiles" {
			multi = true
		}
		title, _ := req["title"].(string)
		defPath, _ := req["default_path"].(string)
		paths, err := l.bridge.OpenDialog(NativeDialogOptions{
			Title:       title,
			DefaultPath: defPath,
			Directory:   isDir,
			Multiple:    multi,
		})
		if err != nil {
			return nil, err
		}
		if len(paths) == 0 {
			return map[string]any{"canceled": true, "paths": []string{}}, nil
		}
		if !isDir {
			selID, count, err := l.selections.RememberFiles(paths)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"canceled":     false,
				"path":         paths[0],
				"paths":        paths,
				"selection_id": selID,
				"count":        count,
			}, nil
		}
		return map[string]any{"canceled": false, "path": paths[0], "paths": paths}, nil
	case "saveFile":
		if l.bridge == nil {
			return nil, errors.New("当前界面不支持保存对话框")
		}
		title, _ := req["title"].(string)
		defPath, _ := req["default_path"].(string)
		dest, err := l.bridge.SaveDialog(NativeDialogOptions{Title: title, DefaultPath: defPath})
		if err != nil {
			return nil, err
		}
		return map[string]any{"canceled": dest == "", "path": dest}, nil
	case "uploadHostFiles":
		target, _ := req["target"].(string)
		id, _ := req["id"].(string)
		selID, _ := req["selection_id"].(string)
		paths, ok := l.selections.Take(selID)
		if !ok || len(paths) == 0 {
			return nil, errors.New("文件选择已过期，请重新选择")
		}
		args := append([]string{target, id}, paths...)
		return l.client.RunFileCommand(ctx, "upload-files", args...)
	case "desktopFile":
		id, _ := req["id"].(string)
		return l.client.RunFileCommand(ctx, "desktop-file", id)
	case "browserFile":
		op, _ := req["operation"].(string)
		id, _ := req["id"].(string)
		if dest, _ := req["destination"].(string); dest != "" {
			return l.client.RunFileCommand(ctx, "browser-file", op, id, dest)
		}
		return l.client.RunFileCommand(ctx, "browser-file", op, id)
	case "shareProject":
		mode, _ := req["mode"].(string)
		dir, _ := req["directory"].(string)
		return l.client.RunFileCommand(ctx, "share-project", mode, dir)
	case "appUpdate", "update":
		if l.bridge == nil {
			return map[string]any{"phase": "manual"}, nil
		}
		op, _ := req["operation"].(string)
		if op == "" {
			op = "status"
		}
		return l.bridge.HandleUpdate(op), nil
	case "clientPreferences":
		return l.handleClientPreferences(req), nil
	case "trialGuestPassword":
		op, _ := req["operation"].(string)
		if op == "save" {
			pw, _ := req["password"].(string)
			if err := macnative.StoreTrialGuestPassword(pw); err != nil {
				return nil, err
			}
			return map[string]any{"saved": true}, nil
		}
		if op == "consume" {
			pw, err := macnative.ConsumeTrialGuestPassword()
			if err != nil {
				return nil, err
			}
			return map[string]any{"password": pw}, nil
		}
		return nil, fmt.Errorf("不支持的试用密码操作：%s", op)
	case "workbenchReady":
		if l.bridge != nil {
			l.bridge.MarkWorkbenchReady()
		}
		return true, nil
	case "focusWindow":
		if l.bridge != nil {
			l.bridge.ShowAndFocusWindow()
		}
		return true, nil
	case "openExternal":
		urlStr, _ := req["url"].(string)
		if l.bridge != nil && urlStr != "" {
			return true, l.bridge.OpenExternal(urlStr)
		}
		return false, nil
	case "openPath":
		p, _ := req["path"].(string)
		if l.bridge != nil && p != "" {
			return true, l.bridge.OpenPath(p)
		}
		return false, nil
	case "showItemInFolder":
		p, _ := req["path"].(string)
		if l.bridge != nil && p != "" {
			return true, l.bridge.ShowItemInFolder(p)
		}
		return false, nil
	default:
		return nil, fmt.Errorf("未支持的原生操作：%s", action)
	}
}

func (l *AgentNativeLoop) handleAccountDevice(ctx context.Context, req map[string]any) (any, error) {
	op, _ := req["operation"].(string)
	kind, _ := req["kind"].(string)
	binding, _ := req["binding"].(string)
	if binding == "" && l.store != nil {
		binding = l.store.Snapshot().Account.DeviceBinding
	}
	switch op {
	case "", "status":
		devMap := map[string]string{}
		if rawDevs, ok := req["devices"].(map[string]any); ok {
			for k, v := range rawDevs {
				if vs, ok := v.(string); ok {
					devMap[k] = vs
				}
			}
		} else if l.store != nil {
			maps.Copy(devMap, l.store.Snapshot().Account.Devices)
		}
		return l.devices.Status(binding, devMap), nil
	case "enroll":
		if kind == "" {
			return nil, errors.New("缺少快捷登录类型")
		}
		if id, _ := req["id"].(string); id != "" {
			secret, _ := req["secret"].(string)
			if err := macnative.StoreAccountDevice(kind, binding, id, secret); err != nil {
				return nil, err
			}
			return l.devices.Status(binding, map[string]string{kind: id}), nil
		}
		return l.devices.Enroll(ctx, kind, binding)
	case "revoke":
		return l.devices.Revoke(ctx, kind, binding)
	case "unlock":
		reason, _ := req["reason"].(string)
		view, err := l.devices.Unlock(ctx, kind, binding, reason)
		if err != nil {
			return nil, err
		}
		if l.store != nil {
			l.store.TriggerRefresh()
		}
		return view, nil
	default:
		return nil, fmt.Errorf("未知的设备登录操作：%s", op)
	}
}

func (l *AgentNativeLoop) handleClientPreferences(req map[string]any) any {
	if l.store == nil {
		return DefaultClientPreferences()
	}
	op, _ := req["operation"].(string)
	if op == "set" {
		l.store.UpdateClientPreferences(func(p *ClientPreferences) {
			if v, ok := req["audio_muted"].(bool); ok {
				p.AudioMuted = v
			}
			if v, ok := req["audio_volume"].(float64); ok && v >= 0 && v <= 1 {
				p.AudioVolume = v
			}
			if v, ok := req["theme_mode"].(string); ok && v != "" {
				p.ThemeMode = v
			}
			if v, ok := req["show_diagnostics"].(bool); ok {
				p.ShowDiagnostics = v
			}
		})
	}
	return l.store.Snapshot().ClientPrefs
}

func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	default:
		return 0, false
	}
}
