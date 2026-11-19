package microvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/exitpolicy"
)

func TestPTYFramingRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	openReq, _ := json.Marshal(PTYOpenRequest{Cols: 120, Rows: 40, Workdir: "/workspace", Command: []string{"claude"}})
	if err := WritePTYFrame(&buf, PTYMsgOpen, openReq); err != nil {
		t.Fatalf("WritePTYFrame open: %v", err)
	}
	if err := WritePTYFrame(&buf, PTYMsgData, []byte("hello world")); err != nil {
		t.Fatalf("WritePTYFrame data: %v", err)
	}
	if err := WritePTYFrame(&buf, PTYMsgResize, EncodePTYResize(140, 50)); err != nil {
		t.Fatalf("WritePTYFrame resize: %v", err)
	}
	if err := WritePTYFrame(&buf, PTYMsgExit, EncodePTYExit(0, "")); err != nil {
		t.Fatalf("WritePTYFrame exit: %v", err)
	}

	kind, payload, err := ReadPTYFrame(&buf)
	if err != nil || kind != PTYMsgOpen {
		t.Fatalf("read open: %v kind=%d", err, kind)
	}
	var gotOpen PTYOpenRequest
	if err := json.Unmarshal(payload, &gotOpen); err != nil || gotOpen.Cols != 120 || gotOpen.Rows != 40 {
		t.Fatalf("unmarshal open: %v %+v", err, gotOpen)
	}

	kind, payload, err = ReadPTYFrame(&buf)
	if err != nil || kind != PTYMsgData || string(payload) != "hello world" {
		t.Fatalf("read data: %v %d %q", err, kind, string(payload))
	}

	kind, payload, err = ReadPTYFrame(&buf)
	if err != nil || kind != PTYMsgResize {
		t.Fatalf("read resize: %v %d", err, kind)
	}
	c, r, err := DecodePTYResize(payload)
	if err != nil || c != 140 || r != 50 {
		t.Fatalf("decode resize: %v %d %d", err, c, r)
	}

	kind, payload, err = ReadPTYFrame(&buf)
	if err != nil || kind != PTYMsgExit {
		t.Fatalf("read exit: %v %d", err, kind)
	}
	code, _, err := DecodePTYExit(payload)
	if err != nil || code != 0 {
		t.Fatalf("decode exit: %v %d", err, code)
	}

	if err := WritePTYFrame(&buf, PTYMsgData, make([]byte, PTYMaxFrameSize+1)); err == nil {
		t.Fatal("expected oversized frame to be rejected")
	}
}

type mockControlHarness struct {
	gate            *cage.EgressGate
	canaryStatus    string
	faultOverrideOK bool
	mu              sync.Mutex
	healthCalls     int
}

func (m *mockControlHarness) dial(_ context.Context, port uint32) (net.Conn, error) {
	serverConn, clientConn := net.Pipe()
	switch port {
	case PortControl:
		srv := &http.Server{Handler: m.handler()}
		go func() {
			_ = srv.Serve(&singleConnListener{conn: serverConn})
		}()
	case PortPTY:
		go func() {
			defer serverConn.Close()
			kind, payload, err := ReadPTYFrame(serverConn)
			if err != nil || kind != PTYMsgOpen {
				return
			}
			var req PTYOpenRequest
			_ = json.Unmarshal(payload, &req)
			_ = WritePTYFrame(serverConn, PTYMsgData, []byte("ok:"+req.Workdir))
			_ = WritePTYFrame(serverConn, PTYMsgExit, EncodePTYExit(0, ""))
		}()
	case PortBrowser:
		go func() {
			defer serverConn.Close()
			buf := make([]byte, 32)
			n, err := serverConn.Read(buf)
			if err != nil {
				return
			}
			_, _ = serverConn.Write([]byte("tunnel:" + string(buf[:n])))
		}()
	default:
		_ = serverConn.Close()
		_ = clientConn.Close()
		return nil, errors.New("unsupported port")
	}
	return clientConn, nil
}

func (m *mockControlHarness) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.healthCalls++
		ready := m.healthCalls >= 2
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(HealthResponse{Ready: ready, PID: 1, Hostname: "LAT5450-A7K2M9"})
	})
	mux.HandleFunc("POST /bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /mounts/add", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /browser/start", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(BrowserStatusResponse{Running: true, VNCReady: true, CDPReady: true, PID: 42})
	})
	mux.HandleFunc("POST /browser/stop", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /probe", func(w http.ResponseWriter, r *http.Request) {
		var req ProbeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Stage == "boundary" {
			// Exercise real EgressGate denials so counters increment authentically.
			_ = m.gate.Allows("tcp", mustAddrPort("1.1.1.1:443"))
			_ = m.gate.Allows("udp", mustAddrPort("1.1.1.1:3478"))
			_, _ = m.gate.ExchangeDNS(context.Background(), mustDNSQuestion("nonce.example.com."))

			off := 0
			_ = json.NewEncoder(w).Encode(ProbeResult{
				TZ:                  "Etc/UTC",
				Lang:                "en_US.UTF-8",
				LCAll:               "en_US.UTF-8",
				LocaleValid:         true,
				UTCOffsetExpected:   &off,
				UTCOffsetActual:     &off,
				UTCOffsetSystem:     &off,
				IPv6DefaultRoutes:   0,
				IPv6GlobalAddresses: 0,
				UDPAttempted:        true,
				DNSUDPAttempted:     true,
				Resolvers:           ResolverResult{Count: 1, Confined: true},
				PrivateFile:         m.canaryStatus,
			})
			return
		}
		if req.Stage == "network" {
			st := m.gate.Status()
			if st.Fault {
				if m.faultOverrideOK {
					conn1, _ := m.gate.DialContext(context.Background(), "tcp", cage.DiagnosticAddress)
					writeDiagReq(conn1, "/egress")
					conn2, _ := m.gate.DialContext(context.Background(), "tcp", cage.DiagnosticAddress)
					writeDiagReq(conn2, "/dns")
					_ = json.NewEncoder(w).Encode(ProbeResult{
						Egress: NetworkProbeResult{Status: http.StatusServiceUnavailable},
						DNS:    NetworkProbeResult{Status: http.StatusServiceUnavailable},
					})
					return
				}
				_ = json.NewEncoder(w).Encode(ProbeResult{
					Egress: NetworkProbeResult{Status: http.StatusOK, IP: "8.8.8.8"},
					DNS:    NetworkProbeResult{Status: http.StatusOK},
				})
				return
			}
			// Drive real diagnostic handlers through EgressGate so ProbeTCP & ProbeDNS increment offline.
			conn1, _ := m.gate.DialContext(context.Background(), "tcp", cage.DiagnosticAddress)
			writeDiagReq(conn1, "/egress")
			conn2, _ := m.gate.DialContext(context.Background(), "tcp", cage.DiagnosticAddress)
			writeDiagReq(conn2, "/dns")
			_ = json.NewEncoder(w).Encode(ProbeResult{
				Egress: NetworkProbeResult{Status: http.StatusOK, IP: "8.8.8.8"},
				DNS:    NetworkProbeResult{Status: http.StatusOK},
			})
		}
	})
	return mux
}

type singleConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() {
		c = l.conn
		l.done = make(chan struct{})
	})
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *singleConnListener) Close() error {
	if l.done != nil {
		select {
		case <-l.done:
		default:
			close(l.done)
		}
	}
	return nil
}
func (l *singleConnListener) Addr() net.Addr { return &net.TCPAddr{} }

func writeDiagReq(conn net.Conn, path string) {
	if conn == nil {
		return
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET "+path+" HTTP/1.1\r\nHost: 198.18.0.254\r\n\r\n")
	buf := make([]byte, 512)
	_, _ = conn.Read(buf)
}

func mustAddrPort(s string) netip.AddrPort {
	return netip.MustParseAddrPort(s)
}

func mustDNSQuestion(name string) *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	return q
}

func offlineCageConfig(t *testing.T) cage.Config {
	t.Helper()
	// Bind and immediately close an ephemeral loopback port so proxy dial fails instantly offline.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	cfg := testCageConfig()
	cfg.ProxyURL = "http://" + addr
	return cfg
}

func TestGuestClientWaitReadyPTYAndBrowser(t *testing.T) {
	cfg := offlineCageConfig(t)
	gate, err := cage.NewEgressGate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close("done")

	h := &mockControlHarness{gate: gate, canaryStatus: "unreachable", faultOverrideOK: true}
	client := NewGuestClient(h.dial)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	health, err := client.WaitReady(ctx)
	if err != nil || !health.Ready || health.Hostname != "LAT5450-A7K2M9" {
		t.Fatalf("WaitReady: %v %+v", err, health)
	}
	if err := client.Bootstrap(ctx, BootstrapConfig{Hostname: "LAT5450-A7K2M9", Timezone: "Etc/UTC"}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if err := client.Mount(ctx, MountRequest{ID: "project", MountPoint: "/workspace"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	ptyConn, err := client.OpenPTY(ctx, PTYOpenRequest{Cols: 80, Rows: 24, Workdir: "/workspace"})
	if err != nil {
		t.Fatalf("OpenPTY: %v", err)
	}
	kind, payload, err := ReadPTYFrame(ptyConn)
	if err != nil || kind != PTYMsgData || string(payload) != "ok:/workspace" {
		t.Fatalf("unexpected PTY frame: %v %d %q", err, kind, string(payload))
	}
	_ = ptyConn.Close()

	bStatus, err := client.StartBrowser(ctx, BrowserStartRequest{URL: "https://claude.ai"})
	if err != nil || !bStatus.Running {
		t.Fatalf("StartBrowser: %v %+v", err, bStatus)
	}
	bConn, err := client.DialBrowserChannel(ctx, "vnc")
	if err != nil {
		t.Fatalf("DialBrowserChannel: %v", err)
	}
	buf := make([]byte, 64)
	n, _ := bConn.Read(buf)
	_ = bConn.Close()
	if string(buf[:n]) != "tunnel:vnc\n" {
		t.Fatalf("unexpected browser tunnel reply: %q", string(buf[:n]))
	}
	if err := client.StopBrowser(ctx); err != nil {
		t.Fatalf("StopBrowser: %v", err)
	}
}

func TestRunSafetyCheckPassAndFailureCases(t *testing.T) {
	usExit := func(_ context.Context, _ cage.Config) (exitpolicy.Location, error) {
		return exitpolicy.Location{
			IP:          "8.8.8.8",
			CountryCode: "US",
			Timezone:    "America/New_York",
			CheckedAt:   time.Now(),
		}, nil
	}
	cnExit := func(_ context.Context, _ cage.Config) (exitpolicy.Location, error) {
		return exitpolicy.Location{
			IP:          "223.5.5.5",
			CountryCode: "CN",
			Timezone:    "Asia/Shanghai",
			CheckedAt:   time.Now(),
		}, nil
	}

	// 1. Passing case grants GateOpen with all 11 mandatory checks passed.
	cfg := offlineCageConfig(t)
	gate, err := cage.NewEgressGate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close("done")
	h := &mockControlHarness{gate: gate, canaryStatus: "unreachable", faultOverrideOK: true}
	client := NewGuestClient(h.dial)

	report, err := RunSafetyCheck(context.Background(), cfg, gate, client, usExit, "/Users/host/.ssh/id_ed25519")
	if err != nil {
		t.Fatalf("expected passing safety check, got error: %v", err)
	}
	if st := gate.Status(); st.Mode != cage.GateOpen {
		t.Fatalf("expected GateOpen after passing check, got %q (%s)", st.Mode, st.Reason)
	}
	if len(report.Checks) != 11 {
		t.Fatalf("expected 11 safety checks, got %d", len(report.Checks))
	}

	// 2. CN exit rejected by ExitPolicy keeps gate closed.
	gateCN, err := cage.NewEgressGate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gateCN.Close("done")
	hCN := &mockControlHarness{gate: gateCN, canaryStatus: "unreachable", faultOverrideOK: true}
	if _, err := RunSafetyCheck(context.Background(), cfg, gateCN, NewGuestClient(hCN.dial), cnExit, "/Users/host/.ssh/id_ed25519"); err == nil {
		t.Fatal("expected CN exit check to fail")
	}
	if st := gateCN.Status(); st.Mode != cage.GateClosed {
		t.Fatalf("expected GateClosed on CN exit, got %q", st.Mode)
	}

	// 3. Fail-closed violation (200 under SetFault(true)) keeps gate closed.
	gateFault, err := cage.NewEgressGate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gateFault.Close("done")
	hFault := &mockControlHarness{gate: gateFault, canaryStatus: "unreachable", faultOverrideOK: false}
	if _, err := RunSafetyCheck(context.Background(), cfg, gateFault, NewGuestClient(hFault.dial), usExit, "/Users/host/.ssh/id_ed25519"); err == nil {
		t.Fatal("expected broken fail-closed drill to fail")
	}
	if st := gateFault.Status(); st.Mode != cage.GateClosed {
		t.Fatalf("expected GateClosed on fail-closed violation, got %q", st.Mode)
	}

	// 4. Readable host canary keeps gate closed.
	gateCanary, err := cage.NewEgressGate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gateCanary.Close("done")
	hCanary := &mockControlHarness{gate: gateCanary, canaryStatus: "readable", faultOverrideOK: true}
	if _, err := RunSafetyCheck(context.Background(), cfg, gateCanary, NewGuestClient(hCanary.dial), usExit, "/Users/host/.ssh/id_ed25519"); err == nil {
		t.Fatal("expected readable host canary to fail")
	}
	if st := gateCanary.Status(); st.Mode != cage.GateClosed {
		t.Fatalf("expected GateClosed on readable canary, got %q", st.Mode)
	}
}

func TestMonitorExitRevokesOnDrift(t *testing.T) {
	cfg := offlineCageConfig(t)
	gate, err := cage.NewEgressGate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close("done")

	h := &mockControlHarness{gate: gate, canaryStatus: "unreachable", faultOverrideOK: true}
	usExit := func(_ context.Context, _ cage.Config) (exitpolicy.Location, error) {
		return exitpolicy.Location{IP: "8.8.8.8", CountryCode: "US", CheckedAt: time.Now()}, nil
	}
	report, err := RunSafetyCheck(context.Background(), cfg, gate, NewGuestClient(h.dial), usExit, "/Users/host/.ssh/id_ed25519")
	if err != nil {
		t.Fatalf("RunSafetyCheck: %v", err)
	}

	var calls atomic.Int32
	driftProbe := func(_ context.Context, _ cage.Config) (exitpolicy.Location, error) {
		if calls.Add(1) == 1 {
			return exitpolicy.Location{IP: "8.8.8.8", CountryCode: "US", CheckedAt: time.Now()}, nil
		}
		return exitpolicy.Location{IP: "223.5.5.5", CountryCode: "CN", CheckedAt: time.Now()}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		MonitorExit(ctx, cfg, gate, report.ID, driftProbe, 25*time.Millisecond)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("MonitorExit did not stop on CN drift")
	}
	if st := gate.Status(); st.Mode != cage.GateClosed {
		t.Fatalf("expected GateClosed after exit drift, got %q", st.Mode)
	}
}
