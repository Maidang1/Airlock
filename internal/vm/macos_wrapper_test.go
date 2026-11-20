//go:build darwin && arm64 && cgo && !no_vz

package vm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestprofile"
	"github.com/sylvinus/agent-vm/internal/limaembed"
)

// The wrapper tests the Lima backend keeps for macOS guests: what used to be
// covered against a Debian instance (lima_test.go, deleted with the Linux
// environments) still holds for the virtual Macs it now creates.

// macosWrapperLima is a Lima over a fresh home, prepared as the server
// prepares it: the embedded share written, Lima's own _config refused.
func macosWrapperLima(t *testing.T) *Lima {
	t.Helper()
	t.Setenv("LIMA_TEMPLATES_PATH", "")
	l, err := NewLima(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.prepare(); errors.Is(err, limaembed.ErrNoAssets) {
		t.Skip("built without Lima's templates and guest agent: build it with make")
	} else if err != nil {
		t.Fatal(err)
	}
	return l
}

// macosWrapperTemplate is what CreateMacOSFromIPSW hands Create: the
// installation template, with a restore image that is never booted.
func macosWrapperTemplate(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "cage-macos-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	_, err = fmt.Fprintf(f, macOSInstallTemplate, `"/example.ipsw"`, "sha256:"+strings.Repeat("a", 64))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func macosWrapperCreate(t *testing.T, l *Lima, name string, s Settings) {
	t.Helper()
	if err := l.Create(t.Context(), name, macosWrapperTemplate(t), s, io.Discard); err != nil {
		t.Fatal(err)
	}
}

// Local Linux environments run as MicroVMs: a Linux template is refused with
// one message, and nothing is made.
func TestMacOSCreateRefusesNonDarwinTemplate(t *testing.T) {
	l := macosWrapperLima(t)
	err := l.Create(t.Context(), "linux-12345678", "template:debian-13", Settings{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "Lima creates only macOS guests") {
		t.Fatalf("a Linux template was accepted: %v", err)
	}
	if _, err := l.Get(t.Context(), "linux-12345678"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused template left a VM behind: %v", err)
	}
}

// Lima's default.yaml and override.yaml would add to what the wrapper sets
// for every guest: Create and Start both refuse while either is there.
func TestMacOSCreateRefusesLimaUserConfig(t *testing.T) {
	l := macosWrapperLima(t)
	home := os.Getenv("LIMA_HOME")
	cfg := filepath.Join(home, filenames.ConfigDir)
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	defaultYAML := filepath.Join(cfg, filenames.Default)
	if err := os.WriteFile(defaultYAML, []byte("cpus: 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found := LimaOverrides(home); len(found) != 1 || found[0] != defaultYAML {
		t.Fatalf("overrides: %v", found)
	}
	tmpl := macosWrapperTemplate(t)
	if err := l.Create(t.Context(), "mac", tmpl, Settings{}, io.Discard); !errors.Is(err, ErrLimaOverrides) {
		t.Fatalf("create with a default.yaml: %v", err)
	}
	override := filepath.Join(cfg, filenames.Override)
	if err := os.WriteFile(override, []byte("cpus: 8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found := LimaOverrides(home); len(found) != 2 || found[1] != override {
		t.Fatalf("overrides: %v", found)
	}
	if err := l.Start(t.Context(), "mac", io.Discard); !errors.Is(err, ErrLimaOverrides) {
		t.Fatalf("start with an override.yaml: %v", err)
	}
	os.Remove(defaultYAML)
	os.Remove(override)
	macosWrapperCreate(t, l, "mac", Settings{})
}

// Create refuses a name that already has a VM.
func TestMacOSCreateRefusesDuplicateName(t *testing.T) {
	l := macosWrapperLima(t)
	macosWrapperCreate(t, l, "mac", Settings{})
	err := l.Create(t.Context(), "mac", macosWrapperTemplate(t), Settings{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a duplicate name was accepted: %v", err)
	}
}

// What the user's _config/base.yaml says is not mixed into a new guest, and
// the wrapper's own containerd stays out of it.
func TestMacOSCreateSkipsUserBaseYAML(t *testing.T) {
	l := macosWrapperLima(t)
	cfg := filepath.Join(os.Getenv("LIMA_HOME"), filenames.ConfigDir)
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "base.yaml"), []byte("cpus: 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	macosWrapperCreate(t, l, "mac", Settings{CPUs: 2, MemoryGiB: 4, DiskGiB: 64, NoContainerd: true})
	p, err := l.Get(t.Context(), "mac")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != Stopped || p.CPUs != 2 || p.Memory != 4<<30 || p.Disk != 64<<30 {
		t.Fatalf("created: %+v", p)
	}
	inst, err := l.inspect(t.Context(), "mac")
	if err != nil {
		t.Fatal(err)
	}
	if *inst.Config.Containerd.User || *inst.Config.Containerd.System {
		t.Fatal("the guest runs the wrapper's own containerd")
	}
}

// A cage configuration makes the guest its own machine: no host proxy
// variables carried in, its timezone, and no guest profile invented.
func TestMacOSCreateWithCageConfigIsolatesTheGuest(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(cage.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cage.ConfigEnv, configPath)
	l := macosWrapperLima(t)
	macosWrapperCreate(t, l, "mac", Settings{})
	inst, err := l.inspect(t.Context(), "mac")
	if err != nil {
		t.Fatal(err)
	}
	cfg := inst.Config
	if *cfg.PropagateProxyEnv {
		t.Fatal("the guest inherits the host's proxy environment")
	}
	for _, key := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		if v, ok := cfg.Env[key]; !ok || v != "" {
			t.Fatalf("proxy variable %s reached the guest: %q", key, v)
		}
	}
	if *cfg.TimeZone != "Etc/UTC" {
		t.Fatalf("timezone: %q", *cfg.TimeZone)
	}
	if *cfg.User.Name != "cageprovision" || *cfg.User.Home != "/Users/cageprovision" ||
		*cfg.User.Comment != "Cage Provisioning" || *cfg.User.UID != 501 {
		t.Fatalf("guest account: %+v", cfg.User)
	}
	if *cfg.Video.Display != "offscreen" {
		t.Fatalf("display: %q", *cfg.Video.Display)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(configPath), guestprofile.Filename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("guest creation assigned a profile of its own: %v", err)
	}
}

// Edit applies the settings it is given, appends the rule that leaves the
// guest's ephemeral ports unforwarded without touching the guest's own
// rules, and writes a whole lima.yaml or nothing.
func TestMacOSEditSettingsAndEphemeralPortRule(t *testing.T) {
	l := macosWrapperLima(t)
	macosWrapperCreate(t, l, "mac", Settings{})
	port := 23456
	if err := l.Edit(t.Context(), "mac", Settings{CPUs: 3, SSHLocalPort: &port}); err != nil {
		t.Fatal(err)
	}
	p, err := l.Get(t.Context(), "mac")
	if err != nil {
		t.Fatal(err)
	}
	if p.CPUs != 3 || p.SSHPortConfig != port {
		t.Fatalf("edited: %+v", p)
	}
	if err := l.Edit(t.Context(), "mac", Settings{noEphemeralPorts: true}); err != nil {
		t.Fatal(err)
	}
	inst, err := l.inspect(t.Context(), "mac")
	if err != nil {
		t.Fatal(err)
	}
	if len(inst.Config.PortForwards) != 2 || !inst.Config.PortForwards[0].Ignore ||
		inst.Config.PortForwards[0].GuestPortRange != [2]int{1, 65535} {
		t.Fatalf("the template's own forwarding rule was lost: %+v", inst.Config.PortForwards)
	}
	rule := &inst.Config.PortForwards[1]
	if rule.GuestPortRange != ephemeralPorts || !rule.Ignore || rule.Proto != limatype.ProtoAny ||
		rule.GuestIP.String() != "0.0.0.0" || *rule.GuestIPMustBeZero {
		t.Fatalf("ephemeral port rule: %+v", rule)
	}
	yamlPath := filepath.Join(p.Dir, filenames.LimaYAML)
	before, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	// Once there, not written again; and a Settings with nothing to change
	// touches nothing either.
	if err := l.Edit(t.Context(), "mac", Settings{noEphemeralPorts: true}); err != nil {
		t.Fatal(err)
	}
	if err := l.Edit(t.Context(), "mac", Settings{}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("an unchanged edit rewrote lima.yaml:\n%s", after)
	}
}

// A settings that does not validate leaves the guest's lima.yaml untouched.
func TestMacOSEditRefusedLeavesYAMLUntouched(t *testing.T) {
	l := macosWrapperLima(t)
	macosWrapperCreate(t, l, "mac", Settings{})
	p, err := l.Get(t.Context(), "mac")
	if err != nil {
		t.Fatal(err)
	}
	yamlPath := filepath.Join(p.Dir, filenames.LimaYAML)
	before, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	tooHigh := 70000
	err = l.Edit(t.Context(), "mac", Settings{SSHLocalPort: &tooHigh})
	if err == nil || !strings.Contains(err.Error(), "ssh.localPort") {
		t.Fatalf("an invalid port was accepted: %v", err)
	}
	after, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a refused edit changed lima.yaml")
	}
}

// Edits at once each write a whole lima.yaml, and leave nothing behind.
func TestMacOSEditConcurrent(t *testing.T) {
	l := macosWrapperLima(t)
	macosWrapperCreate(t, l, "mac", Settings{})
	var wg sync.WaitGroup
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Edit(t.Context(), "mac", Settings{CPUs: i}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	p, err := l.Get(t.Context(), "mac")
	if err != nil || p.Status == Broken {
		t.Fatalf("after: %v %+v", err, p)
	}
	ents, err := os.ReadDir(p.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

// macosWrapperInstance is a stopped macOS guest of the driver named, made by
// hand: Create needs Apple's restore image, the PID files do not.
func macosWrapperInstance(t *testing.T, l *Lima, name, vmType string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("LIMA_HOME"), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	yaml := "os: Darwin\narch: x86_64\nvmType: " + vmType +
		"\nimages:\n  - location: /example.ipsw\n    arch: x86_64\n"
	if err := os.WriteFile(filepath.Join(dir, filenames.LimaYAML), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// macosWrapperSpawn is a process idling under the name of a program and with
// the options of a QEMU: of this VM's, or any other.
func macosWrapperSpawn(t *testing.T, dir, prog, vmName string) (int, chan struct{}) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip(err)
	}
	// Not under t.TempDir(): its name embeds this test's, and isQEMU would
	// read "qemu" out of the program's path instead of the program's name.
	binDir, err := os.MkdirTemp("", "drv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(binDir) })
	exe := filepath.Join(binDir, prog)
	if err := os.Symlink(sh, exe); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-c", "while :; do sleep 1; done",
		"-name", "lima-"+vmName, "-pidfile", filepath.Join(dir, filenames.PIDFile(limatype.QEMU)))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	return cmd.Process.Pid, done
}

func macosWrapperSurvives(t *testing.T, exited chan struct{}) bool {
	t.Helper()
	select {
	case <-exited:
		return false
	case <-time.After(200 * time.Millisecond):
		return true
	}
}

// Only a QEMU outlives the wrapper's process that serves it, and only the one
// of this VM: a hostagent without its driver may be booting the guest, and a
// PID file left by a QEMU that aborted may name another program's process
// now. Stop kills exactly the orphan.
func TestMacOSOrphanedQEMUStop(t *testing.T) {
	ctx := t.Context()
	t.Setenv("LIMA_TEMPLATES_PATH", "")
	l, err := NewLima(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	macosWrapperInstance(t, l, "o", "qemu")
	// Lima reports the instance dir with its symlinks resolved: the PID files
	// and the QEMU's -pidfile must be spelled the same, or the process is not
	// recognized as this VM's.
	inst, err := l.inspect(ctx, "o")
	if err != nil {
		t.Fatal(err)
	}
	dir := inst.Dir
	qemuPID := filepath.Join(dir, filenames.PIDFile(limatype.QEMU))
	haPID := filepath.Join(dir, filenames.HostAgentPID)
	writePID := func(path string, pid int) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A driver running without the wrapper's process: orphaned.
	pid, exited := macosWrapperSpawn(t, dir, "qemu-system-fake", "o")
	writePID(qemuPID, pid)
	got, err := l.Get(ctx, "o")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Broken || !got.Orphaned {
		t.Fatalf("a driver without its hostagent: %+v", got)
	}
	if err := l.Stop(ctx, "o"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the orphaned driver still runs")
	}
	if got, _ = l.Get(ctx, "o"); got.Status != Stopped || got.Orphaned {
		t.Fatalf("after Stop: %+v", got)
	}

	// A hostagent without its driver: not orphaned, it may be booting the
	// guest. Stop leaves it alone.
	ha, haExited := macosWrapperSpawn(t, dir, "hostagent-fake", "o")
	writePID(haPID, ha)
	if got, _ = l.Get(ctx, "o"); got.Status != Broken || got.Orphaned {
		t.Fatalf("a hostagent without its driver: %+v", got)
	}
	l.Stop(ctx, "o")
	if !macosWrapperSurvives(t, haExited) {
		t.Fatal("Stop killed a hostagent without its driver")
	}
	os.Remove(haPID)

	// A PID file whose number is another program's now, or another VM's
	// QEMU: not this VM's orphan, and Stop leaves it alone.
	for _, c := range []struct{ prog, vm string }{{"not-a-vm", "o"}, {"qemu-system-fake", "other"}} {
		other, otherExited := macosWrapperSpawn(t, dir, c.prog, c.vm)
		writePID(qemuPID, other)
		if got, _ = l.Get(ctx, "o"); got.Orphaned {
			t.Fatalf("%s of %s: %+v", c.prog, c.vm, got)
		}
		l.Stop(ctx, "o")
		if !macosWrapperSurvives(t, otherExited) {
			t.Fatalf("Stop killed %s of %s", c.prog, c.vm)
		}
	}
}
