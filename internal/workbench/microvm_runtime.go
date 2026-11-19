package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestprofile"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
	"github.com/sylvinus/agent-vm/internal/microvm"
)

// The controller owns the VM. Image files contain no host directory grants;
// each project is registered only by the existing authenticated project API.
type microVMRuntime struct {
	imageDir string
	mu       sync.Mutex
	vm       *microvm.VM
	gate     *cage.EgressGate
	shares   *microvm.SFTPServer
	stopGate func()
}

func (m *microVMRuntime) running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.vm != nil && m.vm.Running()
}

func (m *microVMRuntime) client() (*microvm.GuestClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.vm == nil {
		return nil, errTerminalNotReady
	}
	return m.vm.Client(), nil
}

func (s *Server) loadMicroVM() error {
	image := os.Getenv("AIRLOCK_MICROVM_IMAGE_DIR")
	bindingPath := filepath.Join(s.profile, "microvm.json")
	var binding struct {
		ImageDir string `json:"image_dir"`
	}
	if data, err := os.ReadFile(bindingPath); err == nil {
		if err := json.Unmarshal(data, &binding); err != nil {
			return err
		}
		if binding.ImageDir == "" {
			return errors.New("MicroVM image binding is empty")
		}
		if image == "" {
			image = binding.ImageDir
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if image == "" {
		return nil
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("MicroVM development requires Apple Silicon macOS")
	}
	image, err := filepath.Abs(image)
	if err != nil {
		return err
	}
	for _, name := range []string{"vmlinuz", "rootfs.img", "data.img"} {
		info, err := os.Stat(filepath.Join(image, name))
		if err != nil {
			return fmt.Errorf("MicroVM image %s: %w; run make microvm-image first", name, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("invalid MicroVM image: %s", name)
		}
	}
	// Never reinterpret a profile already bound to a different runtime.
	for _, name := range []string{"macos-guest.json", "windows-guest.json", "container.json", "state/lima"} {
		if _, err := os.Stat(filepath.Join(s.profile, name)); err == nil {
			return errors.New("MicroVM needs a separate development profile; use make dev-microvm")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	s.microGuest = &microVMRuntime{imageDir: image}
	if err := os.MkdirAll(s.instanceDir(), 0700); err != nil {
		return err
	}
	binding.ImageDir = image
	return writePrivate(bindingPath, binding)
}

func (s *Server) startMicroVM() (returned error) {
	if s.microGuest.running() {
		return s.attachMicroVM()
	}
	// Clean up a guest that stopped independently before creating new devices.
	if err := s.stopMicroVM(); err != nil {
		return err
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	if cfg.GuestProfile == nil {
		cfg.GuestProfile, err = guestprofile.Create(s.profile, guestprofile.Options{})
		if err != nil {
			return err
		}
	}
	// A development guest can boot offline before a proxy is configured. The
	// gate stays closed until the same mandatory checks authorize networking.
	if cfg.RequireManagedProxy(s.profile) == nil {
		p, err := managedproxy.Load(s.profile)
		if err != nil {
			return err
		}
		if !managedproxy.Running(s.operationContext(), s.profile, p) {
			if err = s.activateProxy(p, cfg); err != nil {
				return err
			}
			cfg, err = loadConfig(s.configPath)
			if err != nil {
				return err
			}
		}
	}
	gate, err := cage.NewEgressGate(cfg)
	if err != nil {
		return err
	}
	m := s.microGuest
	disk := filepath.Join(s.instanceDir(), "data.img")
	if _, err = os.Stat(disk); os.IsNotExist(err) {
		// APFS clone keeps the private writable disk separate from image outputs.
		staging, err := os.CreateTemp(s.instanceDir(), ".data-*")
		if err != nil {
			return err
		}
		_ = staging.Close()
		defer os.Remove(staging.Name())
		if err = exec.CommandContext(s.operationContext(), "cp", "-c", filepath.Join(m.imageDir, "data.img"), staging.Name()).Run(); err != nil {
			return fmt.Errorf("create development data disk: %w", err)
		}
		if err = os.Rename(staging.Name(), disk); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	var identity struct {
		Guest   string `json:"guest"`
		Gateway string `json:"gateway"`
	}
	identityPath := filepath.Join(s.instanceDir(), "network.json")
	if data, e := os.ReadFile(identityPath); e == nil {
		if err = json.Unmarshal(data, &identity); err != nil {
			return err
		}
	} else if os.IsNotExist(e) {
		identity.Guest, err = microvm.RandomLocalMAC()
		if err != nil {
			return err
		}
		identity.Gateway, err = microvm.RandomLocalMAC()
		if err != nil {
			return err
		}
		if err = writePrivate(identityPath, identity); err != nil {
			return err
		}
	} else {
		return e
	}
	allocation, err := cage.LoadResourceAllocation(s.configPath)
	if err != nil {
		return err
	}
	memory, cpus, err := allocation.Limits()
	if err != nil {
		return err
	}
	initrd := filepath.Join(m.imageDir, "initrd.img")
	if _, err = os.Stat(initrd); os.IsNotExist(err) {
		initrd = ""
	} else if err != nil {
		return err
	}
	shares := microvm.NewSFTPServer()
	machine, err := microvm.NewVM(microvm.VMConfig{
		KernelPath: filepath.Join(m.imageDir, "vmlinuz"), InitrdPath: initrd,
		RootfsPath: filepath.Join(m.imageDir, "rootfs.img"), DataDiskPath: disk,
		ConsoleLogPath: filepath.Join(s.profile, "microvm-console.log"),
		CPUs:           cpus, MemoryBytes: uint64(memory) << 30, GuestMAC: identity.Guest, GatewayMAC: identity.Gateway,
		Gate: gate, SFTP: shares,
	})
	if err != nil {
		gate.Close("VM configuration failed")
		return err
	}
	stopGate, err := cage.StartGateControl(s.configPath, s.instanceDir(), gate)
	if err != nil {
		_ = machine.Stop(context.Background())
		return err
	}
	defer func() {
		if returned != nil {
			stopGate()
			_ = machine.Stop(context.Background())
		}
	}()
	s.setMessage("正在从本地镜像启动 MicroVM…")
	ctx, cancel := context.WithTimeout(s.operationContext(), 60*time.Second)
	defer cancel()
	if err = machine.Start(ctx); err != nil {
		return err
	}
	client := machine.Client()
	if _, err = client.WaitReady(ctx); err != nil {
		return fmt.Errorf("%w; console: %s", err, filepath.Join(s.profile, "microvm-console.log"))
	}
	if err = client.Bootstrap(ctx, microvm.BootstrapConfig{
		Hostname: cfg.GuestProfile.Hostname, Timezone: cfg.Timezone, Locale: cfg.Locale, Language: cfg.BrowserLanguage,
		WorkUser: "work", BrowserUser: "browser", GuestIP: microvm.DefaultGuestIP, GatewayIP: microvm.DefaultGatewayIP, NetmaskBits: 24,
	}); err != nil {
		return err
	}
	if err = cfg.RecordRunningAt(s.instanceDir()); err != nil {
		return err
	}
	m.mu.Lock()
	m.vm, m.gate, m.shares, m.stopGate = machine, gate, shares, stopGate
	m.mu.Unlock()
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
	s.publishGate(gate.Status())
	s.restoreProjectShares()
	// Startup success describes a usable offline guest; networking is separate.
	if err := s.checkNetworkIfWanted(); err != nil {
		s.mu.Lock()
		s.safety.Error = err.Error()
		s.mu.Unlock()
	}
	return nil
}

func (s *Server) attachMicroVM() error {
	client, err := s.microGuest.client()
	if err != nil {
		return err
	}
	ctx, cancel := s.controlContext()
	defer cancel()
	h, err := client.Health(ctx)
	s.mu.Lock()
	s.ready = err == nil && h.Ready
	s.mu.Unlock()
	return err
}

func (s *Server) stopMicroVM() error {
	m := s.microGuest
	m.mu.Lock()
	machine, gate, stopGate := m.vm, m.gate, m.stopGate
	m.mu.Unlock()
	if gate != nil {
		gate.Close("MicroVM stopping")
		s.publishGate(gate.Status())
	}
	s.mu.Lock()
	s.ready = false
	s.mu.Unlock()
	s.terminals.closeAll()
	s.closeProjectShares()
	var err error
	if machine != nil {
		if machine.Running() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = s.microVMGuestCommand(ctx, "sync")
			cancel()
		}
		err = machine.Stop(context.Background())
	}
	if err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(s.instanceDir(), "cage-running.json"))
	if stopGate != nil {
		stopGate()
	}
	m.mu.Lock()
	m.vm, m.gate, m.shares, m.stopGate = nil, nil, nil, nil
	m.mu.Unlock()
	return err
}

func (s *Server) checkMicroVMNetwork() error {
	s.safetyOp.Lock()
	defer s.safetyOp.Unlock()
	s.mu.Lock()
	paused := s.networkPaused
	s.mu.Unlock()
	if paused {
		return nil
	}
	client, err := s.microGuest.client()
	if err != nil {
		return err
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	if err = cfg.RequireManagedProxy(s.profile); err != nil {
		return err
	}
	m := s.microGuest
	m.mu.Lock()
	gate := m.gate
	m.mu.Unlock()
	if gate == nil {
		return errTerminalNotReady
	}
	ctx, cancel := context.WithTimeout(s.operationContext(), 90*time.Second)
	s.mu.Lock()
	s.checkCancel = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); s.checkCancel = nil; s.mu.Unlock() }()
	canary, err := os.CreateTemp(s.profile, ".microvm-canary-")
	if err != nil {
		return err
	}
	_ = canary.Close()
	defer os.Remove(canary.Name())
	report, err := microvm.RunSafetyCheck(ctx, cfg, gate, client, microvm.DefaultExitProbe, canary.Name())
	s.publishReport(report)
	s.publishGate(gate.Status())
	s.mu.Lock()
	if err != nil {
		s.safety.Error = err.Error()
	} else {
		s.safety.Error = ""
		s.safety.VerifiedAt = time.Now()
	}
	s.mu.Unlock()
	return err
}

func (s *Server) monitorMicroVMNetwork() {
	if !s.monitorSafetyOp.TryLock() {
		return
	}
	defer s.monitorSafetyOp.Unlock()
	s.mu.Lock()
	ready, paused, busy, last := s.ready, s.networkPaused, s.networkMonitorBlockedLocked(), s.safety.VerifiedAt
	s.mu.Unlock()
	m := s.microGuest
	m.mu.Lock()
	gate := m.gate
	m.mu.Unlock()
	if gate == nil {
		return
	}
	s.publishGate(gate.Status())
	if !ready || paused || busy || time.Since(last) < 30*time.Second {
		return
	}
	status := gate.Status()
	// Never reopen a closed gate from a monitor tick or interrupt active
	// connections with another failure drill. Explicit resume runs all checks.
	if status.Mode != cage.GateOpen || status.Report == nil {
		return
	}
	s.safetyOp.Lock()
	defer s.safetyOp.Unlock()
	ctx, cancel := context.WithTimeout(s.operationContext(), 15*time.Second)
	defer cancel()
	cfg, err := loadConfig(s.configPath)
	if err == nil && cfg.Fingerprint() != status.Configuration {
		err = errors.New("MicroVM configuration changed; check networking again")
	}
	if err == nil {
		loc, probeErr := microvm.DefaultExitProbe(ctx, cfg)
		err = probeErr
		if err == nil {
			state, message := cfg.ExitPolicy.Evaluate(loc.IP, &loc)
			if state != cage.CheckPassed {
				err = errors.New(message)
			}
		}
		if err == nil {
			err = gate.Renew(status.Report.ID)
		}
	}
	if err != nil {
		gate.Close("出口巡检失败：" + err.Error())
	}
	s.publishGate(gate.Status())
	s.mu.Lock()
	s.safety.VerifiedAt = time.Now()
	if err != nil {
		s.safety.Error = err.Error()
	}
	s.mu.Unlock()
}

func (s *Server) microVMResourceStatus(w http.ResponseWriter, r *http.Request) {
	a, err := cage.LoadResourceAllocation(s.configPath)
	if err != nil {
		requestError(w, err)
		return
	}
	memory, cpus, err := a.Limits()
	if err != nil {
		requestError(w, err)
		return
	}
	maxMemory, maxCPUs := cage.ResourceLimits()
	jsonReply(w, map[string]any{"runtime": "microvm", "running": s.vmRunning(), "automatic": a.Automatic,
		"memory_gib": memory, "cpus": cpus, "max_memory_gib": maxMemory, "max_cpus": maxCPUs,
		"configured_memory_bytes": uint64(memory) << 30, "configured_cpus": cpus,
		"recommended_memory_bytes": uint64(memory) << 30, "recommended_cpus": cpus})
}
