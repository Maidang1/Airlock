//go:build !(darwin && arm64 && cgo && !no_vz)

package microvm

import (
	"context"
	"errors"
	"net"

	"github.com/sylvinus/agent-vm/internal/cage"
)

// VMConfig configures a direct-boot Apple Virtualization Linux MicroVM.
type VMConfig struct {
	KernelPath          string
	InitrdPath          string
	RootfsPath          string
	DataDiskPath        string
	ConsoleLogPath      string
	CPUs                int
	MemoryBytes         uint64
	BaselineMemoryBytes uint64
	GuestMAC            string
	GatewayMAC          string
	Gate                *cage.EgressGate
	SFTP                *SFTPServer
}

// VM is unavailable on non-Apple-Silicon targets.
type VM struct{}

var errUnsupportedPlatform = errors.New("microvm Apple Virtualization backend requires darwin/arm64 with cgo")

func NewVM(VMConfig) (*VM, error)                               { return nil, errUnsupportedPlatform }
func (*VM) Start(context.Context) error                         { return errUnsupportedPlatform }
func (*VM) DialVsock(context.Context, uint32) (net.Conn, error) { return nil, errUnsupportedPlatform }
func (*VM) SetMemoryTarget(context.Context, uint64) error       { return errUnsupportedPlatform }
func (*VM) ReclaimMemory(context.Context) error                 { return errUnsupportedPlatform }
func (*VM) Client() *GuestClient                                { return nil }
func (*VM) Running() bool                                       { return false }
func (*VM) Netstack() *Netstack                                 { return nil }
func (*VM) Stop(context.Context) error                          { return nil }
