//go:build darwin && arm64 && cgo && !no_vz

package microvm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/Code-Hex/vz/v3"
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

// VM manages one lightweight direct-boot Linux MicroVM without Lima.
type VM struct {
	cfg      VMConfig
	netstack *Netstack
	machine  *vz.VirtualMachine

	mu           sync.Mutex
	started      bool
	stopped      bool
	netCancel    context.CancelFunc
	hostNetConn  net.Conn
	vzNetFile    *os.File
	sftpListener *vz.VirtioSocketListener
	client       *GuestClient
}

// NewVM validates cfg and prepares the in-process netstack, VZ virtual devices,
// and confined networking without attaching VZNAT.
func NewVM(cfg VMConfig) (*VM, error) {
	if cfg.Gate == nil {
		return nil, errors.New("microvm requires a non-nil EgressGate")
	}
	for _, p := range []struct {
		name, path string
		required   bool
	}{
		{"kernel", cfg.KernelPath, true},
		{"initrd", cfg.InitrdPath, false},
		{"rootfs", cfg.RootfsPath, true},
		{"data disk", cfg.DataDiskPath, true},
	} {
		if p.path == "" {
			if p.required {
				return nil, fmt.Errorf("missing %s path", p.name)
			}
			continue
		}
		if _, err := os.Stat(p.path); err != nil {
			return nil, fmt.Errorf("cannot access %s %q: %w", p.name, p.path, err)
		}
	}
	if cfg.CPUs <= 0 {
		cfg.CPUs = 2
	}
	if cfg.MemoryBytes == 0 {
		cfg.MemoryBytes = 2 << 30
	}
	if cfg.BaselineMemoryBytes == 0 {
		cfg.BaselineMemoryBytes = 512 << 20
	}
	if cfg.SFTP == nil {
		cfg.SFTP = NewSFTPServer()
	}

	ns, err := NewNetstack(NetstackConfig{
		GatewayMAC: cfg.GatewayMAC,
		GuestMAC:   cfg.GuestMAC,
		Gate:       cfg.Gate,
	})
	if err != nil {
		return nil, err
	}
	cfg.GuestMAC = ns.Config().GuestMAC
	cfg.GatewayMAC = ns.Config().GatewayMAC

	// With the Debian image, forcing console=hvc0 prevents init from becoming
	// reachable. Leave console selection to the kernel.
	// This VM has exactly one NIC; keep its name consistent with guest bootstrap.
	bootOpts := []vz.LinuxBootLoaderOption{
		vz.WithCommandLine("root=/dev/vda ro init=/sbin/airlock-init ipv6.disable=1 net.ifnames=0"),
	}
	if cfg.InitrdPath != "" {
		bootOpts = append(bootOpts, vz.WithInitrd(cfg.InitrdPath))
	}
	bootLoader, err := vz.NewLinuxBootLoader(cfg.KernelPath, bootOpts...)
	if err != nil {
		return nil, err
	}
	vmConfig, err := vz.NewVirtualMachineConfiguration(bootLoader, uint(cfg.CPUs), cfg.MemoryBytes)
	if err != nil {
		return nil, err
	}

	machineID, err := vz.NewGenericMachineIdentifier()
	if err != nil {
		return nil, err
	}
	platform, err := vz.NewGenericPlatformConfiguration(vz.WithGenericMachineIdentifier(machineID))
	if err != nil {
		return nil, err
	}
	vmConfig.SetPlatformVirtualMachineConfiguration(platform)

	rootAtt, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(
		cfg.RootfsPath, true, vz.DiskImageCachingModeCached, vz.DiskImageSynchronizationModeNone,
	)
	if err != nil {
		return nil, err
	}
	rootDev, err := vz.NewVirtioBlockDeviceConfiguration(rootAtt)
	if err != nil {
		return nil, err
	}
	dataAtt, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(
		cfg.DataDiskPath, false, vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeFsync,
	)
	if err != nil {
		return nil, err
	}
	dataDev, err := vz.NewVirtioBlockDeviceConfiguration(dataAtt)
	if err != nil {
		return nil, err
	}
	vmConfig.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{rootDev, dataDev})

	hostConn, vzNetFile, err := CreateDatagramSocketPair()
	if err != nil {
		return nil, err
	}
	netAtt, err := vz.NewFileHandleNetworkDeviceAttachment(vzNetFile)
	if err != nil {
		_ = hostConn.Close()
		_ = vzNetFile.Close()
		return nil, err
	}
	netDev, err := vz.NewVirtioNetworkDeviceConfiguration(netAtt)
	if err != nil {
		_ = hostConn.Close()
		_ = vzNetFile.Close()
		return nil, err
	}
	hwAddr, err := net.ParseMAC(cfg.GuestMAC)
	if err != nil {
		_ = hostConn.Close()
		_ = vzNetFile.Close()
		return nil, err
	}
	vzMAC, err := vz.NewMACAddress(hwAddr)
	if err != nil {
		_ = hostConn.Close()
		_ = vzNetFile.Close()
		return nil, err
	}
	netDev.SetMACAddress(vzMAC)
	vmConfig.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netDev})

	sockDev, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		_ = hostConn.Close()
		_ = vzNetFile.Close()
		return nil, err
	}
	vmConfig.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{sockDev})

	balloonDev, err := vz.NewVirtioTraditionalMemoryBalloonDeviceConfiguration()
	if err != nil {
		_ = hostConn.Close()
		_ = vzNetFile.Close()
		return nil, err
	}
	vmConfig.SetMemoryBalloonDevicesVirtualMachineConfiguration([]vz.MemoryBalloonDeviceConfiguration{balloonDev})

	entropyDev, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		_ = hostConn.Close()
		_ = vzNetFile.Close()
		return nil, err
	}
	vmConfig.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropyDev})

	if cfg.ConsoleLogPath != "" {
		serialAtt, err := vz.NewFileSerialPortAttachment(cfg.ConsoleLogPath, false)
		if err != nil {
			_ = hostConn.Close()
			_ = vzNetFile.Close()
			return nil, err
		}
		consoleDev, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAtt)
		if err != nil {
			_ = hostConn.Close()
			_ = vzNetFile.Close()
			return nil, err
		}
		vmConfig.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{consoleDev})
	}

	if ok, err := vmConfig.Validate(); !ok || err != nil {
		_ = hostConn.Close()
		_ = vzNetFile.Close()
		return nil, fmt.Errorf("invalid VZ microvm configuration: %w", err)
	}
	machine, err := vz.NewVirtualMachine(vmConfig)
	if err != nil {
		_ = hostConn.Close()
		_ = vzNetFile.Close()
		return nil, err
	}

	v := &VM{
		cfg:         cfg,
		netstack:    ns,
		machine:     machine,
		hostNetConn: hostConn,
		vzNetFile:   vzNetFile,
	}
	v.client = NewGuestClient(v.DialVsock)
	return v, nil
}

// Start boots the MicroVM, starts the in-process gvisor-tap-vsock switch, and
// listens for guest vsock:1003 SFTP share connections.
func (v *VM) Start(ctx context.Context) error {
	v.mu.Lock()
	if v.started {
		v.mu.Unlock()
		return errors.New("microvm already started")
	}
	v.started = true
	netCtx, netCancel := context.WithCancel(context.Background())
	v.netCancel = netCancel
	v.mu.Unlock()

	go func() {
		_ = v.netstack.ServeBess(netCtx, v.hostNetConn)
	}()

	if err := v.machine.Start(); err != nil {
		netCancel()
		return err
	}

	deadline := time.Now().Add(15 * time.Second)
	for v.machine.State() != vz.VirtualMachineStateRunning {
		state := v.machine.State()
		if state == vz.VirtualMachineStateError || state == vz.VirtualMachineStateStopped {
			netCancel()
			return fmt.Errorf("microvm failed to reach running state: %v", state)
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			netCancel()
			return errors.New("timed out waiting for microvm running state")
		}
		select {
		case <-ctx.Done():
			netCancel()
			return ctx.Err()
		case newState := <-v.machine.StateChangedNotify():
			if newState == vz.VirtualMachineStateRunning {
				break
			}
		case <-time.After(25 * time.Millisecond):
		}
	}

	socks := v.machine.SocketDevices()
	if len(socks) == 0 {
		netCancel()
		return errors.New("microvm has no virtio-socket device")
	}
	ln, err := socks[0].Listen(PortSFTP)
	if err != nil {
		netCancel()
		return fmt.Errorf("listen vsock:%d: %w", PortSFTP, err)
	}
	v.mu.Lock()
	v.sftpListener = ln
	v.mu.Unlock()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_ = v.cfg.SFTP.HandleConn(c)
			}(conn)
		}
	}()
	return nil
}

// DialVsock opens a connection to the given guest vsock port.
func (v *VM) DialVsock(_ context.Context, port uint32) (net.Conn, error) {
	for _, s := range v.machine.SocketDevices() {
		conn, err := s.Connect(port)
		if err == nil {
			runtime.KeepAlive(v.machine)
			return conn, nil
		}
		return nil, err
	}
	return nil, errors.New("no virtio-socket device available")
}

// SetMemoryTarget adjusts the guest Virtio memory balloon target size in bytes.
func (v *VM) SetMemoryTarget(ctx context.Context, target uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if target < 256<<20 || target > v.cfg.MemoryBytes || target%(1<<20) != 0 {
		return errors.New("memory target is outside valid balloon bounds")
	}
	if v.machine.State() != vz.VirtualMachineStateRunning {
		return errors.New("microvm is not running")
	}
	for _, dev := range v.machine.MemoryBalloonDevices() {
		if balloon := vz.AsVirtioTraditionalMemoryBalloonDevice(dev); balloon != nil {
			// go-vz wraps a borrowed NSArray element with a releasing finalizer;
			// clear it so temporary wrappers never release the VM-owned device.
			runtime.SetFinalizer(balloon, nil)
			balloon.SetTargetVirtualMachineMemorySize(target)
			runtime.KeepAlive(v.machine)
			return nil
		}
	}
	return errors.New("memory balloon device unavailable")
}

// ReclaimMemory shrinks the guest memory target back to BaselineMemoryBytes.
func (v *VM) ReclaimMemory(ctx context.Context) error {
	return v.SetMemoryTarget(ctx, v.cfg.BaselineMemoryBytes)
}

// Client returns the GuestClient wired to this VM's vsock ports.
func (v *VM) Client() *GuestClient {
	return v.client
}

func (v *VM) Running() bool {
	return v.machine != nil && v.machine.State() == vz.VirtualMachineStateRunning
}

// Netstack returns the VM's confined host-side gvisor-tap-vsock instance.
func (v *VM) Netstack() *Netstack {
	return v.netstack
}

// Stop shuts down the guest gracefully (falling back to forced stop after 3s)
// and releases the SFTP listener and network socket pair.
func (v *VM) Stop(_ context.Context) error {
	v.mu.Lock()
	if v.stopped {
		v.mu.Unlock()
		return nil
	}
	ln := v.sftpListener
	netCancel := v.netCancel
	hostConn := v.hostNetConn
	vzFile := v.vzNetFile
	v.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}

	var stopErr error
	if v.machine != nil && v.machine.State() == vz.VirtualMachineStateRunning {
		requested := false
		if v.machine.CanRequestStop() {
			requested, _ = v.machine.RequestStop()
		}
		if requested {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && v.machine.State() != vz.VirtualMachineStateStopped {
				time.Sleep(50 * time.Millisecond)
			}
		}
		if v.machine.State() != vz.VirtualMachineStateStopped && v.machine.CanStop() {
			stopErr = v.machine.Stop()
		}
	}
	if stopErr != nil {
		return stopErr
	}
	v.mu.Lock()
	v.stopped = true
	v.mu.Unlock()

	if netCancel != nil {
		netCancel()
	}
	if hostConn != nil {
		_ = hostConn.Close()
	}
	if vzFile != nil {
		_ = vzFile.Close()
	}
	return stopErr
}
