package workbench

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func TestFailedSubscriptionRefreshKeepsWorkingNodesAndSecretsPrivate(t *testing.T) {
	s := testServer(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			conn.Close()
		}
	}()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := managedproxy.Load(s.profile)
	p.Port = listener.Addr().(*net.TCPAddr).Port
	cfg.ProxyURL = p.Endpoint()
	if err = writePrivate(s.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err = p.Import("source", "https://example.com/sub?token=private-subscription", []byte(`proxies: [{name: qa, type: socks5, server: 127.0.0.1, port: 1080, password: private-node-password}]`), "source", 24, ""); err != nil {
		t.Fatal(err)
	}
	p.Selected = p.Sources[0].Nodes[0].ID
	if err = p.Save(s.profile); err != nil {
		t.Fatal(err)
	}
	before := p.Sources[0]
	if err = s.changeProxy("refresh", proxyInput{ID: "source"}, nil); err == nil {
		t.Fatal("broken subscription accepted")
	}
	after, err := managedproxy.Load(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	if after.Sources[0].Nodes[0].ID != before.Nodes[0].ID || after.Selected != p.Selected || !after.Sources[0].Updated.Equal(before.Updated) || after.Sources[0].Error == "" {
		t.Fatal("failed refresh replaced last good data")
	}
	w := request(s, "GET", "/api/proxy", s.origin, "", true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, secret := range []string{"private-subscription", "private-node-password", p.Auth} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("status leaked proxy credentials")
		}
	}
	if w = request(s, "POST", "/api/proxy/stop", s.origin, `{}`, false); w.Code != 401 {
		t.Fatal("proxy mutation accepted without authentication")
	}
}

func TestProxyPortConflictRejectedAndFreePortPersisted(t *testing.T) {
	s := testServer(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	occupiedPort := listener.Addr().(*net.TCPAddr).Port

	if err := s.changeProxy("port", proxyInput{Port: occupiedPort}, nil); err == nil || !strings.Contains(err.Error(), "端口已被占用") {
		t.Fatalf("expected conflict error for occupied port %d, got %v", occupiedPort, err)
	}
	if err := s.changeProxy("port", proxyInput{Port: 80}, nil); err == nil {
		t.Fatal("expected error for privileged port 80")
	}

	freeProbe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	freePort := freeProbe.Addr().(*net.TCPAddr).Port
	freeProbe.Close()

	// Unselected state saves the port without requiring mihomo startup.
	p, err := managedproxy.Load(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	selected := p.Selected
	p.Selected = ""
	if err := p.Save(s.profile); err != nil {
		t.Fatal(err)
	}
	if err := s.changeProxy("port", proxyInput{Port: freePort}, nil); err != nil {
		t.Fatalf("expected free port %d to save cleanly when unselected, got %v", freePort, err)
	}
	loaded, err := managedproxy.Load(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Port != freePort {
		t.Fatalf("expected saved proxy port %d, got %d", freePort, loaded.Port)
	}

	// Selected state applies and saves via stubbed proxy core hooks.
	loaded.Selected = selected
	if err := loaded.Save(s.profile); err != nil {
		t.Fatal(err)
	}
	oldValidate, oldStart := validateProxyCore, startProxyCore
	t.Cleanup(func() { validateProxyCore, startProxyCore = oldValidate, oldStart })
	validateProxyCore = func(_ context.Context, profile string, st managedproxy.Store) error {
		_, err := st.Runtime(profile)
		return err
	}
	startProxyCore = func(context.Context, string, managedproxy.Store) error { return nil }

	freeProbe2, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	freePort2 := freeProbe2.Addr().(*net.TCPAddr).Port
	freeProbe2.Close()

	if err := s.changeProxy("port", proxyInput{Port: freePort2}, nil); err != nil {
		t.Fatalf("expected free port %d to apply with selected node, got %v", freePort2, err)
	}
	applied, err := managedproxy.Load(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Port != freePort2 {
		t.Fatalf("expected applied proxy port %d, got %d", freePort2, applied.Port)
	}
}

func TestSettingsCannotWriteProxyEndpoint(t *testing.T) {
	s := testServer(t)
	for _, body := range []string{`{"proxy_url":"socks5://127.0.0.1:7890"}`, `{"proxy_username":"user"}`, `{"proxy_password":"secret"}`} {
		if w := request(s, "POST", "/api/settings", s.origin, body, true); w.Code != 400 {
			t.Fatal("removed proxy settings accepted", w.Code)
		}
	}
	if w := request(s, "POST", "/api/proxy/migrate", s.origin, `{}`, true); w.Code != 404 {
		t.Fatal("removed migration entry accepted", w.Code)
	}
}
