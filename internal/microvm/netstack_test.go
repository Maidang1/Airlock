package microvm

import (
	"bufio"
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/exitpolicy"
)

func testCageConfig() cage.Config {
	cfg := cage.DefaultConfig()
	cfg.ProxyRevision = "rev-1"
	cfg.ExitPolicy = exitpolicy.Policy{
		Mode:        "dynamic",
		Scope:       "country",
		CountryCode: "US",
	}
	return cfg
}

func TestNetstackGateInvariants(t *testing.T) {
	cfg := testCageConfig()
	gate, err := cage.NewEgressGate(cfg)
	if err != nil {
		t.Fatalf("NewEgressGate: %v", err)
	}
	defer gate.Close("test done")

	ns, err := NewNetstack(NetstackConfig{Gate: gate})
	if err != nil {
		t.Fatalf("NewNetstack: %v", err)
	}

	// Verify Netstack kept a valid isolated subnet outside 198.18.0.0/15.
	nc := ns.Config()
	if nc.GatewayIP != DefaultGatewayIP || nc.GuestIP != DefaultGuestIP {
		t.Fatalf("unexpected gateway/guest IP: %+v", nc)
	}

	// GateClosed denies public TCP, diagnostic host, and DNS.
	if gate.Allows("tcp", netip.MustParseAddrPort("1.1.1.1:443")) {
		t.Fatal("GateClosed unexpectedly allowed public TCP")
	}
	if gate.Allows("tcp", netip.MustParseAddrPort(cage.DiagnosticAddress)) {
		t.Fatal("GateClosed unexpectedly allowed diagnostic host")
	}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	if _, err := gate.ExchangeDNS(context.Background(), q); err == nil {
		t.Fatal("GateClosed unexpectedly permitted DNS")
	}

	// GateChecking allows ONLY 198.18.0.254:80, still rejecting public TCP/UDP/IPv6/ICMP/private.
	if err := gate.BeginCheck(); err != nil {
		t.Fatalf("BeginCheck: %v", err)
	}
	if !gate.Allows("tcp", netip.MustParseAddrPort(cage.DiagnosticAddress)) {
		t.Fatal("GateChecking denied diagnostic service")
	}
	for _, denied := range []struct {
		net  string
		addr string
	}{
		{"tcp", "1.1.1.1:443"},
		{"udp", "1.1.1.1:3478"},
		{"udp", "1.1.1.1:53"},
		{"icmp", "1.1.1.1:1"},
		{"tcp", "[2606:4700:4700::1111]:443"},
		{"tcp", "127.0.0.1:17896"},
		{"tcp", "10.0.0.1:80"},
		{"tcp", "192.168.1.1:80"},
		{"tcp", "169.254.169.254:80"},
	} {
		if gate.Allows(denied.net, netip.MustParseAddrPort(denied.addr)) {
			t.Fatalf("GateChecking unexpectedly allowed %s %s", denied.net, denied.addr)
		}
	}

	// SetFault(true) forces 503 on diagnostic /egress and /dns without fallback.
	if err := gate.SetFault(true); err != nil {
		t.Fatalf("SetFault(true): %v", err)
	}
	for _, path := range []string{"/egress", "/dns"} {
		conn, err := gate.DialContext(context.Background(), "tcp", cage.DiagnosticAddress)
		if err != nil {
			t.Fatalf("DialContext diagnostic %s: %v", path, err)
		}
		req, _ := http.NewRequest(http.MethodGet, "http://"+cage.DiagnosticHost+path, nil)
		if err := req.Write(conn); err != nil {
			_ = conn.Close()
			t.Fatalf("write diagnostic req %s: %v", path, err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), req)
		_ = conn.Close()
		if err != nil {
			t.Fatalf("read diagnostic resp %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 under SetFault(true) for %s, got %d", path, resp.StatusCode)
		}
	}
	if err := gate.SetFault(false); err != nil {
		t.Fatalf("SetFault(false): %v", err)
	}

	// SyncProxyRevision revokes gate authorization cleanly.
	cfg2 := cfg
	cfg2.ProxyRevision = "rev-2"
	if err := gate.SyncProxyRevision(cfg2); err != nil {
		t.Fatalf("SyncProxyRevision: %v", err)
	}
	if st := gate.Status(); st.Mode != cage.GateClosed || st.Epoch == 0 {
		t.Fatalf("expected GateClosed after SyncProxyRevision, got %+v", st)
	}
}

func TestCreateDatagramSocketPairAndIPv6DropObserver(t *testing.T) {
	cfg := testCageConfig()
	gate, err := cage.NewEgressGate(cfg)
	if err != nil {
		t.Fatalf("NewEgressGate: %v", err)
	}
	defer gate.Close("test done")

	if err := gate.BeginCheck(); err != nil {
		t.Fatalf("BeginCheck: %v", err)
	}
	st := gate.Status()

	gwMAC, _ := RandomLocalMAC()
	guestMAC, _ := RandomLocalMAC()
	ns, err := NewNetstack(NetstackConfig{
		GatewayMAC: gwMAC,
		GuestMAC:   guestMAC,
		Gate:       gate,
	})
	if err != nil {
		t.Fatalf("NewNetstack: %v", err)
	}

	hostConn, vzFile, err := CreateDatagramSocketPair()
	if err != nil {
		t.Fatalf("CreateDatagramSocketPair: %v", err)
	}
	defer vzFile.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ns.ServeBess(ctx, hostConn)
	}()

	// Construct an Ethernet frame containing a link-local IPv6 UDP probe (ff02::2:49417).
	payload := []byte(cage.MacIPv6ProbePrefix + st.RunID + " " + st.CheckID)
	udpLen := 8 + len(payload)
	pkt := make([]byte, 14+40+udpLen)
	dstHW, _ := net.ParseMAC(gwMAC)
	srcHW, _ := net.ParseMAC(guestMAC)
	copy(pkt[0:6], dstHW)
	copy(pkt[6:12], srcHW)
	binary.BigEndian.PutUint16(pkt[12:14], 0x86dd) // EtherType IPv6
	ip6 := pkt[14:]
	ip6[0] = 0x60
	binary.BigEndian.PutUint16(ip6[4:6], uint16(udpLen))
	ip6[6] = 17 // UDP
	ip6[7] = 1  // Hop limit 1
	copy(ip6[8:24], netip.MustParseAddr("fe80::1").AsSlice())
	copy(ip6[24:40], netip.MustParseAddr("ff02::2").AsSlice())
	udp := ip6[40:]
	binary.BigEndian.PutUint16(udp[0:2], 51000)
	binary.BigEndian.PutUint16(udp[2:4], cage.MacIPv6ProbePort)
	binary.BigEndian.PutUint16(udp[4:6], uint16(udpLen))
	binary.BigEndian.PutUint16(udp[6:8], 0x1234)
	copy(udp[8:], payload)

	if _, err := vzFile.Write(pkt); err != nil {
		t.Fatalf("write ethernet frame to vzFile: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := gate.Status().IPv6LinkProbeCheck; got == st.CheckID {
			cancel()
			_ = hostConn.Close()
			<-done
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("DroppedIPv6 observer did not record link probe (status=%+v)", gate.Status())
}
