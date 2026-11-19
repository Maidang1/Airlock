package microvm

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
	"github.com/sylvinus/agent-vm/internal/cage"
)

// NetstackConfig configures the in-process gvisor-tap-vsock network bound to EgressGate.
type NetstackConfig struct {
	Subnet     string
	GatewayIP  string
	GatewayMAC string
	GuestIP    string
	GuestMAC   string
	MTU        int
	Gate       *cage.EgressGate
}

// Netstack runs a confined IPv4-only user-space network stack in the host process.
type Netstack struct {
	vn  *virtualnetwork.VirtualNetwork
	cfg NetstackConfig
}

// RandomLocalMAC generates a locally-administered unicast MAC address.
func RandomLocalMAC() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[0] = (b[0] & 0xfe) | 0x02
	return net.HardwareAddr(b[:]).String(), nil
}

func validateLocalMAC(s string) (string, error) {
	mac, err := net.ParseMAC(s)
	if err != nil || len(mac) != 6 || mac[0]&3 != 2 {
		return "", fmt.Errorf("invalid locally-administered unicast MAC %q", s)
	}
	return mac.String(), nil
}

// NewNetstack creates a virtual network whose traffic, DNS, and IPv6 drops are
// strictly enforced by cfg.Gate without host loopback NAT or port forwarding.
func NewNetstack(cfg NetstackConfig) (*Netstack, error) {
	if cfg.Gate == nil {
		return nil, errors.New("microvm netstack requires a non-nil EgressGate")
	}
	if cfg.Subnet == "" {
		cfg.Subnet = DefaultSubnet
	}
	if cfg.GatewayIP == "" {
		cfg.GatewayIP = DefaultGatewayIP
	}
	if cfg.GuestIP == "" {
		cfg.GuestIP = DefaultGuestIP
	}
	if cfg.MTU <= 0 {
		cfg.MTU = 1500
	}
	_, ipNet, err := net.ParseCIDR(cfg.Subnet)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet %q: %w", cfg.Subnet, err)
	}
	gw := net.ParseIP(cfg.GatewayIP)
	guest := net.ParseIP(cfg.GuestIP)
	if gw == nil || guest == nil || !ipNet.Contains(gw) || !ipNet.Contains(guest) {
		return nil, errors.New("gateway and guest IPs must belong to subnet")
	}
	if cfg.GatewayMAC == "" {
		if cfg.GatewayMAC, err = RandomLocalMAC(); err != nil {
			return nil, err
		}
	} else if cfg.GatewayMAC, err = validateLocalMAC(cfg.GatewayMAC); err != nil {
		return nil, err
	}
	if cfg.GuestMAC == "" {
		if cfg.GuestMAC, err = RandomLocalMAC(); err != nil {
			return nil, err
		}
	} else if cfg.GuestMAC, err = validateLocalMAC(cfg.GuestMAC); err != nil {
		return nil, err
	}
	if cfg.GatewayMAC == cfg.GuestMAC {
		return nil, errors.New("gateway and guest MAC addresses must differ")
	}

	vnCfg := &types.Configuration{
		MTU:               cfg.MTU,
		Subnet:            cfg.Subnet,
		GatewayIP:         cfg.GatewayIP,
		GatewayMacAddress: cfg.GatewayMAC,
		DHCPStaticLeases: map[string]string{
			cfg.GuestIP: cfg.GuestMAC,
		},
		Forwards:          map[string]string{},
		NAT:               map[string]string{},
		Ec2MetadataAccess: false,
		GatewayVirtualIPs: []string{cfg.GatewayIP},
		Outbound:          cfg.Gate.Allows,
		DialContext:       cfg.Gate.DialContext,
		DNSExchange:       cfg.Gate.ExchangeDNS,
		DroppedIPv6:       cfg.Gate.ObserveDroppedIPv6,
	}
	vn, err := virtualnetwork.New(vnCfg)
	if err != nil {
		return nil, err
	}
	return &Netstack{vn: vn, cfg: cfg}, nil
}

// Config returns the resolved netstack configuration.
func (n *Netstack) Config() NetstackConfig {
	return n.cfg
}

// BytesSent reports bytes sent toward the guest link.
func (n *Netstack) BytesSent() uint64 {
	return n.vn.BytesSent()
}

// BytesReceived reports bytes received from the guest link.
func (n *Netstack) BytesReceived() uint64 {
	return n.vn.BytesReceived()
}

// ServeBess pumps bare L2 datagrams between conn and the virtual switch until
// ctx ends or the connection closes.
func (n *Netstack) ServeBess(ctx context.Context, conn net.Conn) error {
	if conn == nil {
		return errors.New("nil datagram connection")
	}
	return n.vn.AcceptBess(ctx, &udpFileConn{Conn: conn})
}

type udpFileConn struct {
	net.Conn
}

func (c *udpFileConn) Read(b []byte) (int, error) {
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		if errors.Is(err, net.ErrClosed) {
			return 0, errors.New("datagram connection closed")
		}
	}
	return c.Conn.Read(b)
}
