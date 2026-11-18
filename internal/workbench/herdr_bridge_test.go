package workbench

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/guestos"
	"github.com/sylvinus/agent-vm/internal/paths"
)

func TestResolveHerdrWorkdirAuthorizesSharedProjectsOnly(t *testing.T) {
	s := testServer(t)
	t.Cleanup(s.stopHerdrBridge)

	base := t.TempDir()
	project := filepath.Join(base, "repo")
	sub := filepath.Join(project, "pkg")
	outside := filepath.Join(base, "secret")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedProject, err := paths.Resolve(project)
	if err != nil {
		t.Fatal(err)
	}
	resolvedSub, err := paths.Resolve(sub)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(s.profile, "projects.json"), sharedProjects{Paths: []string{resolvedProject}}); err != nil {
		t.Fatal(err)
	}

	// Linux guest path + subpath resolution.
	got, err := s.resolveHerdrWorkdir(guestPath(resolvedProject))
	if err != nil || got != resolvedProject {
		t.Fatalf("resolveHerdrWorkdir(linux guest root) = %q err=%v", got, err)
	}
	got, err = s.resolveHerdrWorkdir(guestPath(resolvedProject) + "/pkg")
	if err != nil || got != resolvedSub {
		t.Fatalf("resolveHerdrWorkdir(linux guest sub) = %q err=%v", got, err)
	}
	// Host path within authorized project also resolves.
	got, err = s.resolveHerdrWorkdir(sub)
	if err != nil || got != resolvedSub {
		t.Fatalf("resolveHerdrWorkdir(host sub) = %q err=%v", got, err)
	}
	// Outside or traversal paths must fail.
	if _, err := s.resolveHerdrWorkdir(outside); err == nil {
		t.Fatal("expected outside host directory to be rejected")
	}
	if _, err := s.resolveHerdrWorkdir(guestPath(resolvedProject) + "/../secret"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}

	// Symlink escape inside authorized project must be rejected.
	escape := filepath.Join(resolvedProject, "escape-link")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	if _, err := s.resolveHerdrWorkdir(guestPath(resolvedProject) + "/escape-link"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}

	// macOS guest mount path mapping.
	s.macosGuest = &guestos.MacOSProfile{User: "cageuser"}
	macRoot := macOSProjectPath("cageuser", resolvedProject)
	got, err = s.resolveHerdrWorkdir(macRoot + "/pkg")
	if err != nil || got != resolvedSub {
		t.Fatalf("resolveHerdrWorkdir(macOS guest sub) = %q err=%v", got, err)
	}
}

func TestHerdrBridgeToggleAndRoutes(t *testing.T) {
	s := agentTestController(t)
	t.Cleanup(s.closeBackground)
	t.Cleanup(s.stopHerdrBridge)

	// Disabled by default.
	if payload := s.herdrMCPPayload(); payload != nil {
		t.Fatalf("expected nil MCP payload when disabled, got %+v", payload)
	}
	status := parityDecode[map[string]any](t, parityUI(t, s, http.MethodGet, "/api/herdr-bridge", nil, http.StatusOK))
	if status["enabled"] != false || status["running"] != false {
		t.Fatalf("unexpected disabled status: %+v", status)
	}
	if _, ok := status["host"].(map[string]any); !ok {
		t.Fatalf("expected host status block in /api/herdr-bridge: %+v", status)
	}
	parityUI(t, s, http.MethodPost, "/api/herdr-bridge/install", map[string]any{"method": "bogus"}, http.StatusBadRequest)

	// Enable via Agent settings.patch (also exercises settings parity).
	if _, err := AgentCall(context.Background(), s.configPath, "settings.patch", json.RawMessage(`{"patch":{"herdr_shared_agents":true}}`)); err != nil {
		t.Fatalf("settings.patch enable failed: %v", err)
	}
	if job := awaitJobIdle(t, &s.jobs); job.Error != "" {
		t.Fatalf("settings.patch job failed: %s", job.Error)
	}
	if !s.herdrEnabled() {
		t.Fatal("expected HerdrSharedAgents preference to be enabled")
	}
	payload := s.herdrMCPPayload()
	if payload == nil || !strings.HasPrefix(payload["url"], "http://127.0.0.1:18520/mcp") || payload["token"] == "" {
		t.Fatalf("unexpected MCP payload: %+v", payload)
	}
	cfgArg := s.herdrMCPConfigArg()
	if !strings.Contains(cfgArg, `"herdr"`) || !strings.Contains(cfgArg, payload["token"]) {
		t.Fatalf("unexpected MCP config arg: %s", cfgArg)
	}

	status = parityDecode[map[string]any](t, parityUI(t, s, http.MethodGet, "/api/herdr-bridge", nil, http.StatusOK))
	if status["enabled"] != true || status["running"] != true {
		t.Fatalf("unexpected enabled status: %+v", status)
	}
	hostPortFloat, ok := status["host_port"].(float64)
	if !ok || int(hostPortFloat) <= 0 {
		t.Fatalf("expected valid host_port, got %+v", status["host_port"])
	}

	// Verify live bridge HTTP endpoint rejects unauthenticated requests and serves authenticated MCP tools/list.
	bridgeURL := "http://127.0.0.1:" + jsonNumberToString(int(hostPortFloat)) + "/mcp"
	unauthResp, err := http.Post(bridgeURL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 from bridge without bearer token, got %d", unauthResp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, bridgeURL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+payload["token"])
	req.Header.Set("Content-Type", "application/json")
	authResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	authResp.Body.Close()
	if authResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from bridge with bearer token, got %d", authResp.StatusCode)
	}

	// Closing a non-existent session via workbench API returns 400 cleanly.
	parityUI(t, s, http.MethodPost, "/api/herdr-bridge/close-session", map[string]string{"session_id": "missing"}, http.StatusBadRequest)

	// Disable via UI settings and confirm bridge stops cleanly.
	settingsBody := parityDecode[Settings](t, parityUI(t, s, http.MethodGet, "/api/settings", nil, http.StatusOK))
	settingsBody.HerdrSharedAgents = false
	parityUI(t, s, http.MethodPost, "/api/settings", settingsBody, http.StatusOK)
	if job := awaitJobIdle(t, &s.jobs); job.Error != "" {
		t.Fatalf("POST /api/settings job failed: %s", job.Error)
	}
	if s.herdrEnabled() {
		t.Fatal("expected HerdrSharedAgents to be disabled")
	}
	if s.herdrMCPPayload() != nil {
		t.Fatal("expected nil MCP payload after disabling")
	}
	parityUI(t, s, http.MethodPost, "/api/herdr-bridge/close-session", map[string]string{"session_id": "missing"}, http.StatusConflict)
}

func jsonNumberToString(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
