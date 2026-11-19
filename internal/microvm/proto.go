// Package microvm implements Airlock's lightweight Apple Virtualization Linux
// MicroVM runtime, replacing Lima/SSH with direct-boot Linux, in-process
// gvisor-tap-vsock wired to EgressGate, and unified virtio-vsock channels.
package microvm

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// PortControl carries guest bootstrap, health, safety probes, mounts, and on-demand browser control.
	PortControl uint32 = 1001
	// PortPTY carries framed interactive terminal sessions over vsock.
	PortPTY uint32 = 1002
	// PortSFTP carries confined project filesystem SFTP streams over vsock.
	PortSFTP uint32 = 1003
	// PortBrowser carries on-demand browser VNC and CDP audit streams over vsock.
	PortBrowser uint32 = 1004

	// DefaultSubnet intentionally stays outside 198.18.0.0/15 so 198.18.0.254
	// (cage.DiagnosticHost) routes through the gateway into EgressGate.
	DefaultSubnet    = "192.168.105.0/24"
	DefaultGatewayIP = "192.168.105.1"
	DefaultGuestIP   = "192.168.105.2"
)

// BootstrapConfig configures guest identity, timezone, locale, and networking on boot.
type BootstrapConfig struct {
	Hostname    string `json:"hostname"`
	Timezone    string `json:"timezone"`
	Locale      string `json:"locale"`
	Language    string `json:"language"`
	WorkUser    string `json:"work_user"`
	BrowserUser string `json:"browser_user"`
	GuestIP     string `json:"guest_ip"`
	GatewayIP   string `json:"gateway_ip"`
	NetmaskBits int    `json:"netmask_bits"`
}

// HealthResponse reports guest init readiness and memory telemetry.
type HealthResponse struct {
	Ready          bool   `json:"ready"`
	PID            int    `json:"pid"`
	Hostname       string `json:"hostname"`
	Timezone       string `json:"timezone"`
	BrowserRunning bool   `json:"browser_running"`
	MemTotalBytes  uint64 `json:"mem_total_bytes"`
	MemAvailBytes  uint64 `json:"mem_avail_bytes"`
}

// ProbeRequest asks airlock-init to execute native boundary or network checks.
type ProbeRequest struct {
	Stage      string `json:"stage"`
	Timezone   string `json:"timezone,omitempty"`
	GatewayIP  string `json:"gateway_ip,omitempty"`
	ProxyHost  string `json:"proxy_host,omitempty"`
	ProxyPort  int    `json:"proxy_port,omitempty"`
	Nonce      string `json:"nonce,omitempty"`
	PrivateFile string `json:"private_file,omitempty"`
	SharedFile string `json:"shared_file,omitempty"`
	Challenge  string `json:"challenge,omitempty"`
}

// ResolverResult summarizes guest /etc/resolv.conf confinement.
type ResolverResult struct {
	Count    int  `json:"count"`
	Confined bool `json:"confined"`
}

// NetworkProbeResult captures an HTTP response from the host diagnostic target.
type NetworkProbeResult struct {
	Status int    `json:"status"`
	IP     string `json:"ip,omitempty"`
}

// ProbeResult captures guest observations for the 11 mandatory safety checks.
type ProbeResult struct {
	TZ                     string             `json:"tz"`
	Lang                   string             `json:"lang"`
	LCAll                  string             `json:"lc_all"`
	SystemTimezone         string             `json:"system_timezone"`
	LocaleValid            bool               `json:"locale_valid"`
	LocaltimeTarget        string             `json:"localtime_target,omitempty"`
	UTCOffsetExpected      *int               `json:"utc_offset_expected,omitempty"`
	UTCOffsetActual        *int               `json:"utc_offset_actual,omitempty"`
	UTCOffsetSystem        *int               `json:"utc_offset_system,omitempty"`
	PublicConnected        bool               `json:"public_connected"`
	LinkLocalConnected     bool               `json:"link_local_connected"`
	ProxyEndpointConnected bool               `json:"proxy_endpoint_connected"`
	IPv6Connected          bool               `json:"ipv6_connected"`
	IPv6DefaultRoutes      int                `json:"ipv6_default_routes"`
	IPv6GlobalAddresses    int                `json:"ipv6_global_addresses"`
	UDPAttempted           bool               `json:"udp_attempted"`
	DNSUDPAttempted        bool               `json:"dns_udp_attempted"`
	DNSTCPConnected        bool               `json:"dns_tcp_connected"`
	DoTConnected           bool               `json:"dot_connected"`
	PublicDNSAnswered      bool               `json:"public_dns_answered"`
	Resolvers              ResolverResult     `json:"resolvers"`
	PrivateFile            string             `json:"private_file"`
	SharedFile             bool               `json:"shared_file"`
	Egress                 NetworkProbeResult `json:"egress"`
	DNS                    NetworkProbeResult `json:"dns"`
	BrowserRunning         bool               `json:"browser_running"`
}

// MountRequest dynamically mounts a host-confined SFTP share inside the guest.
type MountRequest struct {
	ID         string `json:"id"`
	MountPoint string `json:"mount_point"`
	ReadOnly   bool   `json:"read_only"`
}

// UnmountRequest dynamically unmounts a project directory inside the guest.
type UnmountRequest struct {
	ID         string `json:"id"`
	MountPoint string `json:"mount_point"`
}

// SFTPHandshake identifies which registered host share a vsock:1003 stream requests.
type SFTPHandshake struct {
	MountID string `json:"mount_id"`
}

// BrowserStartRequest launches the isolated on-demand browser inside the guest.
type BrowserStartRequest struct {
	URL      string `json:"url,omitempty"`
	Language string `json:"language,omitempty"`
	Timezone string `json:"timezone,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

// BrowserStatusResponse reports the state of the guest on-demand browser.
type BrowserStatusResponse struct {
	Running  bool   `json:"running"`
	VNCReady bool   `json:"vnc_ready"`
	CDPReady bool   `json:"cdp_ready"`
	PID      int    `json:"pid,omitempty"`
	URL      string `json:"url,omitempty"`
}

// Binary PTY framing message types carried over PortPTY.
const (
	PTYMsgOpen   byte = 0x01
	PTYMsgData   byte = 0x02
	PTYMsgResize byte = 0x03
	PTYMsgSignal byte = 0x04
	PTYMsgExit   byte = 0x05

	PTYMaxFrameSize = 256 << 10
)

// PTYOpenRequest is JSON-encoded in the initial PTYMsgOpen frame.
type PTYOpenRequest struct {
	Cols    uint16            `json:"cols"`
	Rows    uint16            `json:"rows"`
	Workdir string            `json:"workdir,omitempty"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// WritePTYFrame writes one length-prefixed PTY protocol frame.
func WritePTYFrame(w io.Writer, msgType byte, payload []byte) error {
	if len(payload) > PTYMaxFrameSize {
		return fmt.Errorf("pty frame payload too large: %d > %d", len(payload), PTYMaxFrameSize)
	}
	var hdr [5]byte
	hdr[0] = msgType
	binary.BigEndian.PutUint32(hdr[1:5], uint32(len(payload)))
	if err := writeFull(w, hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	return writeFull(w, payload)
}

func writeFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// ReadPTYFrame reads one length-prefixed PTY protocol frame.
func ReadPTYFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	msgType := hdr[0]
	n := binary.BigEndian.Uint32(hdr[1:5])
	if n > PTYMaxFrameSize {
		return 0, nil, fmt.Errorf("pty frame exceeds limit: %d", n)
	}
	if n == 0 {
		return msgType, nil, nil
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return msgType, payload, nil
}

// EncodePTYResize encodes terminal dimensions for PTYMsgResize.
func EncodePTYResize(cols, rows uint16) []byte {
	var b [4]byte
	binary.BigEndian.PutUint16(b[0:2], cols)
	binary.BigEndian.PutUint16(b[2:4], rows)
	return b[:]
}

// DecodePTYResize decodes terminal dimensions from PTYMsgResize.
func DecodePTYResize(b []byte) (cols, rows uint16, err error) {
	if len(b) != 4 {
		return 0, 0, errors.New("invalid pty resize payload")
	}
	return binary.BigEndian.Uint16(b[0:2]), binary.BigEndian.Uint16(b[2:4]), nil
}

// EncodePTYExit encodes an exit code and optional message for PTYMsgExit.
func EncodePTYExit(code int32, msg string) []byte {
	raw, _ := json.Marshal(struct {
		Code    int32  `json:"code"`
		Message string `json:"message,omitempty"`
	}{Code: code, Message: msg})
	return raw
}

// DecodePTYExit decodes an exit status payload from PTYMsgExit.
func DecodePTYExit(b []byte) (int32, string, error) {
	var v struct {
		Code    int32  `json:"code"`
		Message string `json:"message,omitempty"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return 1, "", err
	}
	return v.Code, v.Message, nil
}
