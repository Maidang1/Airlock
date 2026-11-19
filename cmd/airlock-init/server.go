package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/microvm"
)

// HostOps abstracts guest OS operations so InitServer and HandleBrowserTunnel
// can be exercised in cross-platform unit tests without Linux syscalls.
type HostOps interface {
	ApplyBootstrap(ctx context.Context, cfg microvm.BootstrapConfig) error
	ConfigureNetwork(ctx context.Context, cfg microvm.BootstrapConfig) error
	ReadMemInfo() (totalBytes, availBytes uint64)
	MountSFTP(ctx context.Context, req microvm.MountRequest) error
	UnmountPath(ctx context.Context, req microvm.UnmountRequest) error
	StartBrowser(ctx context.Context, req microvm.BrowserStartRequest) (microvm.BrowserStatusResponse, error)
	StopBrowser(ctx context.Context) error
	BrowserStatus() microvm.BrowserStatusResponse
	CheckIPv6Routes() (defaultRoutes, globalAddrs int)
	CheckPrivateFile(path string) string
	CheckSharedFile(path, challenge string) bool
	DialLocal(ctx context.Context, network, addr string) (net.Conn, error)
	DialExternal(ctx context.Context, network, addr string) (net.Conn, error)
	SendUDP(addr string, payload []byte) bool
	LookupHost(ctx context.Context, name string) bool
	FetchDiagnostic(ctx context.Context, path string) microvm.NetworkProbeResult
	ReadResolvConf() ([]string, bool)
	ReadTimezoneEnv(expectedTZ string) TZObservation
}

// TZObservation holds guest timezone and locale state observed from files and env.
type TZObservation struct {
	TZ                string
	Lang              string
	LCAll             string
	SystemTimezone    string
	LocaleValid       bool
	LocaltimeTarget   string
	UTCOffsetExpected *int
	UTCOffsetActual   *int
	UTCOffsetSystem   *int
}

// InitServer serves the control/probe/mount/browser HTTP API over PortControl.
type InitServer struct {
	ops HostOps

	mu       sync.RWMutex
	hostname string
	timezone string
	ready    bool
}

// NewInitServer constructs an InitServer backed by ops.
func NewInitServer(ops HostOps) *InitServer {
	hn, _ := os.Hostname()
	return &InitServer{
		ops:      ops,
		hostname: hn,
		ready:    true,
	}
}

// Handler returns the HTTP router exposed on vsock PortControl.
func (s *InitServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /bootstrap", s.handleBootstrap)
	mux.HandleFunc("POST /probe", s.handleProbe)
	mux.HandleFunc("POST /mounts/add", s.handleMountAdd)
	mux.HandleFunc("POST /mounts/remove", s.handleMountRemove)
	mux.HandleFunc("POST /browser/start", s.handleBrowserStart)
	mux.HandleFunc("POST /browser/stop", s.handleBrowserStop)
	mux.HandleFunc("GET /browser/status", s.handleBrowserStatus)
	return mux
}

func (s *InitServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	hn := s.hostname
	tz := s.timezone
	ready := s.ready
	s.mu.RUnlock()

	total, avail := s.ops.ReadMemInfo()
	b := s.ops.BrowserStatus()
	writeJSON(w, http.StatusOK, microvm.HealthResponse{
		Ready:          ready,
		PID:            os.Getpid(),
		Hostname:       hn,
		Timezone:       tz,
		BrowserRunning: b.Running,
		MemTotalBytes:  total,
		MemAvailBytes:  avail,
	})
}

func (s *InitServer) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	var cfg microvm.BootstrapConfig
	if err := decodeJSON(r.Body, &cfg); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if cfg.GuestIP != "" || cfg.GatewayIP != "" {
		if err := s.ops.ConfigureNetwork(r.Context(), cfg); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := s.ops.ApplyBootstrap(r.Context(), cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.mu.Lock()
	if cfg.Hostname != "" {
		s.hostname = cfg.Hostname
	}
	if cfg.Timezone != "" {
		s.timezone = cfg.Timezone
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *InitServer) handleProbe(w http.ResponseWriter, r *http.Request) {
	var req microvm.ProbeRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var out microvm.ProbeResult
	out.BrowserRunning = s.ops.BrowserStatus().Running

	if req.Stage == "boundary" || req.Stage == "environment" {
		tz := s.ops.ReadTimezoneEnv(req.Timezone)
		out.TZ = tz.TZ
		out.Lang = tz.Lang
		out.LCAll = tz.LCAll
		out.SystemTimezone = tz.SystemTimezone
		out.LocaleValid = tz.LocaleValid
		out.LocaltimeTarget = tz.LocaltimeTarget
		out.UTCOffsetExpected = tz.UTCOffsetExpected
		out.UTCOffsetActual = tz.UTCOffsetActual
		out.UTCOffsetSystem = tz.UTCOffsetSystem
	}

	if req.Stage == "boundary" {
		out.PublicConnected = s.tryDial(r.Context(), "tcp", "1.1.1.1:443")
		out.LinkLocalConnected = s.tryDial(r.Context(), "tcp", "169.254.169.254:80")
		if req.ProxyHost != "" && req.ProxyPort > 0 {
			out.ProxyEndpointConnected = s.tryDial(r.Context(), "tcp", net.JoinHostPort(req.ProxyHost, fmt.Sprint(req.ProxyPort)))
		}
		out.IPv6Connected = s.tryDial(r.Context(), "tcp6", "[2606:4700:4700::1111]:443")
		out.IPv6DefaultRoutes, out.IPv6GlobalAddresses = s.ops.CheckIPv6Routes()
		out.UDPAttempted = s.ops.SendUDP("1.1.1.1:3478", []byte("Cage network boundary probe"))
		out.DNSUDPAttempted = s.ops.SendUDP("1.1.1.1:53", []byte("Cage DNS boundary probe"))
		out.DNSTCPConnected = s.tryDial(r.Context(), "tcp", "1.1.1.1:53")
		out.DoTConnected = s.tryDial(r.Context(), "tcp", "1.1.1.1:853")

		servers, readable := s.ops.ReadResolvConf()
		out.Resolvers = checkResolversConfined(servers, readable, req.GatewayIP)

		nonce := req.Nonce
		if nonce == "" {
			nonce = "airlock-probe"
		}
		out.PublicDNSAnswered = s.ops.LookupHost(r.Context(), nonce+".example.com")
		if req.PrivateFile != "" {
			out.PrivateFile = s.ops.CheckPrivateFile(req.PrivateFile)
		} else {
			out.PrivateFile = "unreachable"
		}
		if req.SharedFile != "" && req.Challenge != "" {
			out.SharedFile = s.ops.CheckSharedFile(req.SharedFile, req.Challenge)
		}
	}

	if req.Stage == "network" {
		out.Egress = s.ops.FetchDiagnostic(r.Context(), "/egress")
		out.DNS = s.ops.FetchDiagnostic(r.Context(), "/dns")
	}

	writeJSON(w, http.StatusOK, out)
}

func (s *InitServer) tryDial(ctx context.Context, network, addr string) bool {
	dialCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()
	conn, err := s.ops.DialExternal(dialCtx, network, addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func checkResolversConfined(servers []string, readable bool, gatewayIP string) microvm.ResolverResult {
	if !readable {
		return microvm.ResolverResult{Count: -1, Confined: false}
	}
	allowed := []netip.Prefix{netip.MustParsePrefix("198.18.0.0/15")}
	if gw, err := netip.ParseAddr(gatewayIP); err == nil && gw.Is4() {
		allowed = append(allowed, netip.PrefixFrom(gw, 24).Masked())
	}
	confined := len(servers) > 0
	for _, raw := range servers {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil {
			confined = false
			continue
		}
		if addr.IsLoopback() {
			continue
		}
		matched := false
		for _, prefix := range allowed {
			if prefix.Contains(addr) {
				matched = true
				break
			}
		}
		if !matched {
			confined = false
		}
	}
	return microvm.ResolverResult{Count: len(servers), Confined: confined}
}

func (s *InitServer) handleMountAdd(w http.ResponseWriter, r *http.Request) {
	var req microvm.MountRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.ops.MountSFTP(r.Context(), req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *InitServer) handleMountRemove(w http.ResponseWriter, r *http.Request) {
	var req microvm.UnmountRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.ops.UnmountPath(r.Context(), req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *InitServer) handleBrowserStart(w http.ResponseWriter, r *http.Request) {
	var req microvm.BrowserStartRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	status, err := s.ops.StartBrowser(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *InitServer) handleBrowserStop(w http.ResponseWriter, r *http.Request) {
	if err := s.ops.StopBrowser(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *InitServer) handleBrowserStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.ops.BrowserStatus())
}

// HandleBrowserTunnel splices a PortBrowser stream strictly to localhost VNC
// (15900) or CDP (9222), rejecting any other channel name.
func HandleBrowserTunnel(ctx context.Context, conn net.Conn, dialLocal func(context.Context, string, string) (net.Conn, error)) error {
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(conn, 256)
	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	channel := strings.TrimSpace(line)
	var target string
	switch channel {
	case "vnc":
		target = "127.0.0.1:15900"
	case "cdp":
		target = "127.0.0.1:9222"
	default:
		return fmt.Errorf("disallowed browser tunnel channel %q", channel)
	}
	upstream, err := dialLocal(ctx, "tcp", target)
	if err != nil {
		return err
	}
	defer upstream.Close()
	return spliceConns(ctx, conn, reader, upstream)
}

func spliceConns(ctx context.Context, a net.Conn, aReader *bufio.Reader, b net.Conn) error {
	done := make(chan struct{}, 2)
	stop := context.AfterFunc(ctx, func() {
		_ = a.Close()
		_ = b.Close()
	})
	defer stop()

	go func() {
		_, _ = io.Copy(b, aReader)
		if cw, ok := b.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(a, b)
		if cw, ok := a.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	return nil
}

func decodeJSON(body io.Reader, dst any) error {
	dec := json.NewDecoder(io.LimitReader(body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("expected a single JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
