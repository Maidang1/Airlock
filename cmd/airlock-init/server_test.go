package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/microvm"
)

type fakeHostOps struct {
	mu          sync.Mutex
	bootCfg     microvm.BootstrapConfig
	netCfg      microvm.BootstrapConfig
	mounts      []microvm.MountRequest
	unmounts    []microvm.UnmountRequest
	browser     microvm.BrowserStatusResponse
	resolvers   []string
	resolvOK    bool
	privateStat string
	diagEgress  microvm.NetworkProbeResult
	diagDNS     microvm.NetworkProbeResult
}

func (f *fakeHostOps) ApplyBootstrap(_ context.Context, cfg microvm.BootstrapConfig) error {
	f.mu.Lock()
	f.bootCfg = cfg
	f.mu.Unlock()
	return nil
}

func (f *fakeHostOps) ConfigureNetwork(_ context.Context, cfg microvm.BootstrapConfig) error {
	f.mu.Lock()
	f.netCfg = cfg
	f.mu.Unlock()
	return nil
}

func (f *fakeHostOps) ReadMemInfo() (uint64, uint64) { return 2 << 30, 1536 << 20 }

func (f *fakeHostOps) MountSFTP(_ context.Context, req microvm.MountRequest) error {
	f.mu.Lock()
	f.mounts = append(f.mounts, req)
	f.mu.Unlock()
	return nil
}

func (f *fakeHostOps) UnmountPath(_ context.Context, req microvm.UnmountRequest) error {
	f.mu.Lock()
	f.unmounts = append(f.unmounts, req)
	f.mu.Unlock()
	return nil
}

func (f *fakeHostOps) StartBrowser(_ context.Context, req microvm.BrowserStartRequest) (microvm.BrowserStatusResponse, error) {
	f.mu.Lock()
	f.browser = microvm.BrowserStatusResponse{
		Running:  true,
		VNCReady: true,
		CDPReady: true,
		PID:      1234,
		URL:      req.URL,
	}
	out := f.browser
	f.mu.Unlock()
	return out, nil
}

func (f *fakeHostOps) StopBrowser(_ context.Context) error {
	f.mu.Lock()
	f.browser = microvm.BrowserStatusResponse{}
	f.mu.Unlock()
	return nil
}

func (f *fakeHostOps) BrowserStatus() microvm.BrowserStatusResponse {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.browser
}

func (f *fakeHostOps) CheckIPv6Routes() (int, int)            { return 0, 0 }
func (f *fakeHostOps) CheckPrivateFile(string) string         { return f.privateStat }
func (f *fakeHostOps) CheckSharedFile(string, string) bool    { return false }
func (f *fakeHostOps) DialLocal(context.Context, string, string) (net.Conn, error) {
	return nil, net.ErrClosed
}
func (f *fakeHostOps) DialExternal(context.Context, string, string) (net.Conn, error) {
	return nil, net.ErrClosed
}
func (f *fakeHostOps) SendUDP(string, []byte) bool           { return true }
func (f *fakeHostOps) LookupHost(context.Context, string) bool { return false }
func (f *fakeHostOps) FetchDiagnostic(_ context.Context, path string) microvm.NetworkProbeResult {
	if path == "/dns" {
		return f.diagDNS
	}
	return f.diagEgress
}
func (f *fakeHostOps) ReadResolvConf() ([]string, bool) { return f.resolvers, f.resolvOK }
func (f *fakeHostOps) ReadTimezoneEnv(string) TZObservation {
	off := 0
	return TZObservation{
		TZ:                "Etc/UTC",
		Lang:              "en_US.UTF-8",
		LCAll:             "en_US.UTF-8",
		SystemTimezone:    "Etc/UTC",
		LocaleValid:       true,
		UTCOffsetExpected: &off,
		UTCOffsetActual:   &off,
		UTCOffsetSystem:   &off,
	}
}

func TestInitServerEndpoints(t *testing.T) {
	ops := &fakeHostOps{
		resolvers:   []string{microvm.DefaultGatewayIP},
		resolvOK:    true,
		privateStat: "unreachable",
		diagEgress:  microvm.NetworkProbeResult{Status: 200, IP: "8.8.8.8"},
		diagDNS:     microvm.NetworkProbeResult{Status: 200},
	}
	srv := NewInitServer(ops)
	h := srv.Handler()

	// Bootstrap
	bootBody, _ := json.Marshal(microvm.BootstrapConfig{
		Hostname: "LAT5450-A7K2M9", Timezone: "Etc/UTC", Locale: "en_US.UTF-8",
		GuestIP: microvm.DefaultGuestIP, GatewayIP: microvm.DefaultGatewayIP, NetmaskBits: 24,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/bootstrap", bytes.NewReader(bootBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap failed: %d %s", rec.Code, rec.Body.String())
	}

	// Health reflects bootstrapped hostname/timezone & memory info.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var health microvm.HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil || !health.Ready || health.Hostname != "LAT5450-A7K2M9" {
		t.Fatalf("health unexpected: %v %+v", err, health)
	}

	// Probe boundary & network stages.
	probeBody, _ := json.Marshal(microvm.ProbeRequest{
		Stage: "boundary", Timezone: "Etc/UTC", GatewayIP: microvm.DefaultGatewayIP, PrivateFile: "/Users/host/.ssh/id_ed25519",
	})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/probe", bytes.NewReader(probeBody)))
	var probe microvm.ProbeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &probe); err != nil {
		t.Fatal(err)
	}
	if !probe.Resolvers.Confined || probe.PrivateFile != "unreachable" || probe.PublicConnected || !probe.UDPAttempted {
		t.Fatalf("unexpected boundary probe: %+v", probe)
	}

	// Browser lifecycle
	bBody, _ := json.Marshal(microvm.BrowserStartRequest{URL: "https://claude.ai/oauth/authorize?client_id=1"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/browser/start", bytes.NewReader(bBody)))
	var bStatus microvm.BrowserStatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &bStatus)
	if !bStatus.Running || !bStatus.VNCReady {
		t.Fatalf("browser not running: %+v", bStatus)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/browser/stop", bytes.NewReader([]byte("{}"))))
	if rec.Code != http.StatusOK || ops.BrowserStatus().Running {
		t.Fatal("browser did not stop")
	}

	// Mount / Unmount
	mBody, _ := json.Marshal(microvm.MountRequest{ID: "project", MountPoint: "/workspace"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mounts/add", bytes.NewReader(mBody)))
	if rec.Code != http.StatusOK || len(ops.mounts) != 1 {
		t.Fatalf("mount add failed: %d", rec.Code)
	}
}

func TestCheckResolversConfined(t *testing.T) {
	if r := checkResolversConfined([]string{microvm.DefaultGatewayIP, "127.0.0.1"}, true, microvm.DefaultGatewayIP); !r.Confined || r.Count != 2 {
		t.Fatalf("expected confined resolvers, got %+v", r)
	}
	if r := checkResolversConfined([]string{"8.8.8.8"}, true, microvm.DefaultGatewayIP); r.Confined {
		t.Fatalf("expected public DNS server to be rejected: %+v", r)
	}
	if r := checkResolversConfined(nil, false, microvm.DefaultGatewayIP); r.Confined || r.Count != -1 {
		t.Fatalf("expected unreadable resolv.conf to fail: %+v", r)
	}
}

func TestHandleBrowserTunnelAllowlist(t *testing.T) {
	dialCalled := false
	dialStub := func(_ context.Context, _, target string) (net.Conn, error) {
		dialCalled = true
		c1, c2 := net.Pipe()
		go func() {
			defer c2.Close()
			buf := make([]byte, 32)
			n, _ := c2.Read(buf)
			_, _ = c2.Write([]byte(target + ":" + string(buf[:n])))
		}()
		return c1, nil
	}

	// Disallowed channel must fail before dialing.
	sConn, cConn := net.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- HandleBrowserTunnel(context.Background(), sConn, dialStub)
	}()
	_, _ = cConn.Write([]byte("169.254.169.254:80\n"))
	if err := <-errCh; err == nil || dialCalled {
		t.Fatalf("expected disallowed channel rejection without dialing (err=%v, dialed=%v)", err, dialCalled)
	}
	_ = cConn.Close()

	// Allowed channel "vnc" dials 127.0.0.1:15900 and splices bidirectionally.
	sConn, cConn = net.Pipe()
	go func() {
		_ = HandleBrowserTunnel(context.Background(), sConn, dialStub)
	}()
	_, _ = cConn.Write([]byte("vnc\nRFB"))
	_ = cConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, err := cConn.Read(buf)
	_ = cConn.Close()
	if err != nil || !strings.HasPrefix(string(buf[:n]), "127.0.0.1:15900:RFB") {
		t.Fatalf("expected VNC splice reply, got err=%v data=%q", err, string(buf[:n]))
	}
}
