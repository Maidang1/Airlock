package workbench

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/microvm"
)

func TestMicroVMPTYFrames(t *testing.T) {
	host, guest := net.Pipe()
	defer guest.Close()
	p := newMicroVMPTY(host)
	defer p.Close()
	go func() {
		_ = microvm.WritePTYFrame(guest, microvm.PTYMsgData, []byte("hello\n"))
		_ = microvm.WritePTYFrame(guest, microvm.PTYMsgExit, microvm.EncodePTYExit(0, ""))
	}()
	data, err := io.ReadAll(p)
	if err != nil || string(data) != "hello\n" {
		t.Fatal(string(data), err)
	}
	if err = p.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestMicroVMRealBootAndRuntimeProject(t *testing.T) {
	image := os.Getenv("AIRLOCK_MICROVM_E2E_IMAGE")
	if image == "" {
		t.Skip("set AIRLOCK_MICROVM_E2E_IMAGE to run signed Apple Virtualization test")
	}
	s := testServer(t)
	if err := writePrivate(s.configPath, cage.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	s.networkPaused = true
	t.Setenv("AIRLOCK_MICROVM_IMAGE_DIR", image)
	if err := s.loadMicroVM(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIRLOCK_MICROVM_IMAGE_DIR", "")
	if err := s.loadMicroVM(); err != nil || s.microGuest == nil {
		t.Fatal("persisted image binding was not loaded", err)
	}
	if err := s.prepareEnvironment(); err != nil {
		t.Fatal(err)
	}
	defer s.stopMicroVM()
	if err := s.startMicroVM(); err != nil {
		data, _ := os.ReadFile(filepath.Join(s.profile, "microvm-console.log"))
		t.Log(string(data))
		t.Fatal(err)
	}
	if len(s.projectShares) != 0 {
		t.Fatal("startup shared an unselected directory")
	}
	if mode := s.microGuest.gate.Status().Mode; mode != cage.GateClosed {
		t.Fatal("offline gate opened", mode)
	}
	if owners, err := controllerVMOwners(s.profile); err != nil || len(owners) != 1 {
		t.Fatal("running VM missing from controller ownership", owners, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := func(script string) string {
		t.Helper()
		out, err := s.microVMGuestCommand(ctx, script)
		if err != nil {
			t.Fatalf("command failed: %v output=%s", err, out)
		}
		return string(out)
	}
	if out := command("node --version; npm --version; claude --version; touch ~/persistent-marker"); !strings.Contains(out, "v22.") {
		t.Fatal(out)
	} else {
		t.Log(out)
	}
	project := t.TempDir()
	project, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "host.txt"), []byte("host-visible"), 0600); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"workspace": project, "add": true, "activate": true})
	if reply := request(s, "POST", "/api/projects/open", s.origin, string(payload), true); reply.Code != 200 {
		t.Fatal(reply.Code, reply.Body.String())
	}
	_, guestPath := microVMProjectPath(project)
	if out := command("cat " + guestPath + "/host.txt; printf guest-visible > " + guestPath + "/guest.txt"); !strings.Contains(out, "host-visible") {
		t.Fatal(out)
	}
	if data, err := os.ReadFile(filepath.Join(project, "guest.txt")); err != nil || string(data) != "guest-visible" {
		t.Fatal(string(data), err)
	}
	// Exercise the App's actual terminal endpoint and frame adapter.
	termBody, _ := json.Marshal(map[string]string{"workspace": project, "kind": "shell"})
	reply := request(s, "POST", "/api/terminals", s.origin, string(termBody), true)
	if reply.Code != 200 {
		t.Fatal(reply.Code, reply.Body.String())
	}
	var summary struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	session := s.terminals.get(summary.ID)
	sub := session.subscribe()
	defer sub.Close()
	if err := session.enqueueInput(ctx, "printf 'APP_%s\\n' TERMINAL_OK\n"); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for !strings.Contains(text.String(), "APP_TERMINAL_OK") {
		select {
		case b, ok := <-sub.Output:
			if !ok {
				t.Fatal("terminal closed", text.String())
			}
			text.Write(b)
		case <-ctx.Done():
			t.Fatal(ctx.Err(), text.String())
		}
	}
	if err := s.startMicroVMBrowser(ctx, "about:blank"); err != nil {
		t.Fatal(err)
	}
	if tabs, err := s.microVMBrowserAPI(ctx, "GET", "/tabs", nil); err != nil {
		t.Fatal(err)
	} else {
		t.Log(string(tabs))
		var result struct {
			Tabs []microVMTab `json:"tabs"`
		}
		if err := json.Unmarshal(tabs, &result); err != nil || len(result.Tabs) == 0 {
			t.Fatal("missing browser tab", err)
		}
		if err := s.microVMTabCommand(ctx, result.Tabs[0].ID, "Page.navigate", map[string]string{"url": "data:text/html,<title>MICROVM_BROWSER_OK</title>"}); err != nil {
			t.Fatal(err)
		}
	}
	vnc, err := s.microGuest.vm.Client().DialBrowserChannel(ctx, "vnc")
	if err != nil {
		t.Fatal(err)
	}
	_ = vnc.SetReadDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, 12)
	_, err = io.ReadFull(vnc, header)
	_ = vnc.Close()
	if err != nil || !strings.HasPrefix(string(header), "RFB ") {
		t.Fatal("VNC handshake", string(header), err)
	}
	if err := s.stopMicroVM(); err != nil {
		t.Fatal(err)
	}
	if owners, err := controllerVMOwners(s.profile); err != nil || len(owners) != 0 {
		t.Fatal("stopped VM still owned", owners, err)
	}
	if err := s.startMicroVM(); err != nil {
		t.Fatal(err)
	}
	command("test -f ~/persistent-marker")
	t.Log("real MicroVM boot, offline gate, runtime project read/write, App terminal and home persistence passed")
}
