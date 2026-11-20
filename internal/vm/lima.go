package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/coreos/go-semver/semver"
	"github.com/mattn/go-isatty"
	"github.com/sirupsen/logrus"

	"github.com/lima-vm/lima/v2/pkg/driver/external/server"
	"github.com/lima-vm/lima/v2/pkg/driverutil"
	"github.com/lima-vm/lima/v2/pkg/guestidentity"
	"github.com/lima-vm/lima/v2/pkg/instance"
	"github.com/lima-vm/lima/v2/pkg/limatmpl"
	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/dirnames"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/lima-vm/lima/v2/pkg/limayaml"
	"github.com/lima-vm/lima/v2/pkg/networks/reconcile"
	"github.com/lima-vm/lima/v2/pkg/sshutil"
	"github.com/lima-vm/lima/v2/pkg/store"
	"github.com/lima-vm/lima/v2/pkg/yqutil"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/limaembed"
	"github.com/sylvinus/agent-vm/internal/paths"
)

// Lima runs VMs with the Lima built into agent-vm, as limactl's commands
// do, in this process; its hostagent is this executable again (see
// limaembed.Run).
type Lima struct {
	stateDir string

	once  sync.Once
	share *limaembed.Share
	err   error
}

// NewLima keeps its VMs in home (LIMA_HOME), and writes Lima's guest agent
// and templates under stateDir.
func NewLima(home, stateDir string) (*Lima, error) {
	limaHomeMu.Lock()
	defer limaHomeMu.Unlock()
	if err := os.Setenv("LIMA_HOME", home); err != nil {
		return nil, err
	}
	return &Lima{stateDir: stateDir}, nil
}

// limaHomeMu serializes NewLima's change of LIMA_HOME for Lima's packages.
var limaHomeMu sync.Mutex

// prepare writes the embedded share once, and points Lima's template store
// at it. It refuses Lima's default.yaml and override.yaml, which Lima mixes
// into every VM's config (shares, mount type...): agent-vm's VMs get what
// agent-vm sets, nothing else.
func (l *Lima) prepare() (*limaembed.Share, error) {
	l.once.Do(func() {
		l.share, l.err = limaembed.WriteShare(l.stateDir)
		if l.err == nil {
			l.err = os.Setenv("LIMA_TEMPLATES_PATH", l.share.Templates())
		}
	})
	if l.err != nil {
		return nil, l.err
	}
	home, err := dirnames.LimaDir()
	if err != nil {
		return nil, err
	}
	if found := LimaOverrides(home); len(found) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrLimaOverrides, strings.Join(found, ", "))
	}
	return l.share, nil
}

// LimaOverrides are the default.yaml and override.yaml found in the Lima
// home's _config: shown Host-spelled, as user-facing host paths are.
func LimaOverrides(home string) []string {
	var out []string
	for _, f := range []string{filenames.Default, filenames.Override} {
		p := filepath.Join(home, filenames.ConfigDir, f)
		if _, err := os.Lstat(p); err == nil {
			out = append(out, paths.Host(p))
		}
	}
	return out
}

func (l *Lima) List(ctx context.Context) ([]*Instance, error) {
	names, err := store.Instances()
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []*Instance
	for _, name := range names {
		inst, err := l.Get(ctx, name)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return out, nil
}

func (l *Lima) Get(ctx context.Context, name string) (*Instance, error) {
	inst, err := l.inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	return fromLima(inst), nil
}

func (l *Lima) inspect(ctx context.Context, name string) (*limatype.Instance, error) {
	inst, err := store.Inspect(ctx, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return inst, err
}

func fromLima(inst *limatype.Instance) *Instance {
	out := &Instance{
		Name:         inst.Name,
		Dir:          inst.Dir,
		CPUs:         inst.CPUs,
		Memory:       inst.Memory,
		Disk:         inst.Disk,
		SSHLocalPort: inst.SSHLocalPort,
		SSHAddress:   inst.SSHAddress,
		SSHConfig:    inst.SSHConfigFile,
		VMType:       inst.VMType,
		Arch:         inst.Arch,
	}
	switch inst.Status {
	case limatype.StatusRunning:
		out.Status = Running
	case limatype.StatusStopped:
		out.Status = Stopped
	default:
		out.Status = Broken
	}
	if inst.Config == nil {
		out.Status = Broken
		out.ConfigErr = errors.Join(inst.Errors...)
	}
	// "driver is running but host agent is not". Not the other way around: a
	// hostagent without its driver is also one booting the VM. Only QEMU
	// outlives the hostagent (vz and the others run in it), and the PID file
	// of one that aborted may name another program's process now: Stop kills
	// this one.
	out.Orphaned = out.Status == Broken && inst.Config != nil && inst.HostAgentPID == 0 && inst.DriverPID > 0 &&
		inst.VMType == limatype.QEMU && isQEMU(inst.DriverPID, inst.Dir, inst.Name)
	if c := inst.Config; c != nil && c.SSH.LocalPort != nil {
		out.SSHPortConfig = *c.SSH.LocalPort
	}
	return out
}

// exprs are the yq expressions of s, as limactl's edit flags make them.
func exprs(s Settings) ([]string, error) {
	var e []string
	if s.macOSInstaller != nil {
		e = append(e, fmt.Sprintf(".vmOpts.qemu.macOS.installer = %t", *s.macOSInstaller))
	}
	if s.macOSTimezone != "" {
		e = append(e, ".timezone = "+strconv.Quote(s.macOSTimezone))
	}
	if s.CPUs > 0 {
		e = append(e, fmt.Sprintf(".cpus = %d", s.CPUs))
	}
	if s.MemoryGiB > 0 {
		e = append(e, fmt.Sprintf(".memory = %q", strconv.Itoa(s.MemoryGiB)+"GiB"))
	}
	if s.DiskGiB > 0 {
		e = append(e, fmt.Sprintf(".disk = %q", strconv.Itoa(s.DiskGiB)+"GiB"))
	}
	if s.NoContainerd {
		e = append(e, ".containerd.user = false | .containerd.system = false")
	}
	if s.SSHLocalPort != nil {
		e = append(e, fmt.Sprintf(".ssh.localPort = %d", *s.SSHLocalPort))
	}
	if s.noEphemeralPorts {
		// guestIP 0.0.0.0, not required to be: an ignore rule on Lima's
		// default 127.0.0.1 leaves the listeners on 0.0.0.0 forwarded.
		// Last, after the VM's own rules, which win (Lima takes the first
		// rule that matches); one made earlier is replaced, not repeated.
		e = append(e, fmt.Sprintf(`.portForwards = ((.portForwards // []) | map(select(((.guestPortRange // []) | join("-")) != "%d-%d"))) + [{"guestIP": "0.0.0.0", "guestIPMustBeZero": false, "guestPortRange": [%d, %d], "proto": "any", "ignore": true}]`, ephemeralPorts[0], ephemeralPorts[1], ephemeralPorts[0], ephemeralPorts[1]))
	}
	return e, nil
}

func (l *Lima) Create(ctx context.Context, name, template string, s Settings, log io.Writer) error {
	if _, err := l.prepare(); err != nil {
		return err
	}
	if _, err := store.Inspect(ctx, name); err == nil {
		return fmt.Errorf("VM %s already exists", name)
	}
	return withLog(log, func() error {
		tmpl, err := limatmpl.Read(ctx, name, template)
		if err != nil {
			return err
		}
		// Without _config/base.yaml, which limactl mixes into every template.
		if err := tmpl.Embed(ctx, true, false); err != nil {
			return err
		}
		if err := tmpl.Unmarshal(); err != nil {
			return err
		}
		e, err := exprs(s)
		if err != nil {
			return err
		}
		identity, err := guestidentity.New()
		if err != nil {
			return err
		}
		// Local Linux guests run as MicroVMs; Lima only creates virtual Macs.
		if tmpl.Config.OS == nil || *tmpl.Config.OS != limatype.DARWIN {
			return errors.New("Lima creates only macOS guests; local Linux environments run as MicroVMs")
		}
		if path := os.Getenv(cage.ConfigEnv); path != "" {
			cfg, err := cage.Load(path)
			if err != nil {
				return err
			}
			if profile := cfg.GuestProfile; profile != nil {
				identity.Hostname = profile.Hostname
			}
			tz, _ := json.Marshal(cfg.Timezone)
			e = append(e, ".propagateProxyEnv = false", `.env = {"http_proxy":"","https_proxy":"","ftp_proxy":"","no_proxy":"","HTTP_PROXY":"","HTTPS_PROXY":"","FTP_PROXY":"","NO_PROXY":"","ALL_PROXY":"","all_proxy":""}`, ".timezone = "+string(tz))
			uid := 501
			if tmpl.Config.User.UID != nil && *tmpl.Config.User.UID >= 501 && *tmpl.Config.User.UID <= 599 {
				uid = int(*tmpl.Config.User.UID)
			}
			e = append(e, `.user.name = "cageprovision"`, `.user.home = "/Users/cageprovision"`, `.user.comment = "Cage Provisioning"`, fmt.Sprintf(".user.uid = %d", uid), `.video.display = "offscreen"`)
		}
		data, err := json.Marshal(identity)
		if err != nil {
			return err
		}
		e = append(e, ".guestIdentity = "+string(data))
		if tmpl.Bytes, err = yqutil.EvaluateExpression(ctx, yqutil.Join(e), tmpl.Bytes); err != nil {
			return err
		}
		_, err = instance.Create(ctx, name, tmpl.Bytes, false)
		return err
	})
}

// Edit applies s as `limactl edit` does: the same checks, and nothing is
// written when one fails.
func (l *Lima) Edit(ctx context.Context, name string, s Settings) error {
	inst, err := l.inspect(ctx, name)
	if err != nil {
		return err
	}
	if inst.Status == limatype.StatusRunning {
		return fmt.Errorf("cannot edit the running VM %s", name)
	}
	e, err := exprs(s)
	if err != nil || len(e) == 0 {
		return err
	}
	path := filepath.Join(inst.Dir, filenames.LimaYAML)
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	y, err := yqutil.EvaluateExpression(ctx, yqutil.Join(e), old)
	if err != nil {
		return err
	}
	if bytes.Equal(y, old) {
		return nil
	}
	cfg, err := limayaml.LoadWithWarnings(ctx, y, path)
	if err != nil {
		return err
	}
	if err := driverutil.ResolveVMType(cfg); err != nil {
		return err
	}
	if err := limayaml.Validate(cfg, false); err != nil {
		return err
	}
	inst.Config = cfg
	drv, err := driverutil.CreateConfiguredDriver(ctx, inst, 0)
	if err != nil {
		return err
	}
	defer server.Stop(inst.Dir, true)
	if err := drv.Validate(ctx); err != nil {
		return err
	}
	if err := limayaml.Validate(inst.Config, true); err != nil {
		return err
	}
	if err := limayaml.ValidateAgainstLatestConfig(ctx, y, old); err != nil {
		return err
	}
	// Whole or not at all: a crash midway would leave a truncated lima.yaml.
	// A name of its own, so that two edits do not write the same file.
	f, err := os.CreateTemp(inst.Dir, filenames.LimaYAML+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(y)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// logMu keeps two calls from taking logrus's output from each other.
var logMu sync.Mutex

// withLog runs fn with Lima's logrus output going to log.
// The installer runs with a bounded guest memory so the host keeps its page
// cache for the disk images; 8 GiB is twice Apple's minimum.
const macOSInstallerMemoryGiB = 8

func macOSInstallerActive(config *limatype.LimaYAML) bool {
	if config == nil || config.OS == nil || *config.OS != limatype.DARWIN {
		return false
	}
	var options struct {
		MacOS struct {
			Installer bool `yaml:"installer"`
		} `yaml:"macOS"`
	}
	return limayaml.Convert(config.VMOpts["qemu"], &options, "vmOpts.qemu") == nil && options.MacOS.Installer
}

// Until the guest's SSH server is up, the hostagent reports every refused
// forwarding attempt. They remain in ha.stderr.log but are not progress.
type quietBootLog struct{ io.Writer }

func (q quietBootLog) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("tcpproxy: for incoming conn")) {
		return len(p), nil
	}
	return q.Writer.Write(p)
}

func withLog(log io.Writer, fn func() error) error {
	logMu.Lock()
	defer logMu.Unlock()
	saved := logrus.StandardLogger().Out
	logrus.SetOutput(log)
	defer logrus.SetOutput(saved)
	return fn()
}

func (l *Lima) Start(ctx context.Context, name string, log io.Writer) error {
	share, err := l.prepare()
	if err != nil {
		return err
	}
	inst, err := l.inspect(ctx, name)
	if err != nil {
		return err
	}
	if inst.Status == limatype.StatusRunning {
		return nil
	}
	if inst.Config == nil {
		return errors.Join(inst.Errors...)
	}
	// Here, so that VMs made before it get it too; nothing is written when
	// it is there already.
	settings := Settings{noEphemeralPorts: true}
	if configPath := os.Getenv(cage.ConfigEnv); configPath != "" {
		allocation, loadErr := cage.LoadResourceAllocation(configPath)
		if loadErr != nil {
			return loadErr
		}
		settings.MemoryGiB, settings.CPUs, err = allocation.Limits()
		if *inst.Config.OS == limatype.DARWIN && err == nil {
			cfg, loadErr := cage.Load(configPath)
			if loadErr != nil {
				return loadErr
			}
			settings.macOSTimezone = cfg.Timezone
			requirements, requirementErr := ReadMacOSResourceRequirements(inst.Dir)
			if requirementErr != nil {
				return requirementErr
			}
			settings.MemoryGiB, settings.CPUs, err = requirements.Limits(allocation)
			if err == nil {
				err = saveMacOSResourceRequirements(inst.Dir, requirements)
			}
			// Apple's installer is I/O bound. A large guest only competes with
			// the host page cache; the user's allocation applies after install.
			if err == nil && macOSInstallerActive(inst.Config) && settings.MemoryGiB > macOSInstallerMemoryGiB {
				settings.MemoryGiB = macOSInstallerMemoryGiB
			}
		}
		if err != nil {
			return err
		}
	}
	if err := l.Edit(ctx, name, settings); err != nil {
		return err
	}
	if inst, err = l.inspect(ctx, name); err != nil {
		return err
	}
	if *inst.Config.OS != limatype.DARWIN {
		return fmt.Errorf("VM %s is not a macOS guest; local Linux environments run as MicroVMs", name)
	}
	if len(inst.Config.Mounts) != 0 {
		return fmt.Errorf("VM %s shares host folders, which macOS guests never do", name)
	}
	if err := NetworkConfined(inst.Config); err != nil {
		return err
	}
	// Lima always needs the Darwin helper for first boot.
	guestAgent, err := share.GuestAgent(*inst.Config.OS, *inst.Config.Arch)
	if err != nil {
		return err
	}
	return withLog(quietBootLog{log}, func() error {
		if err := reconcile.Reconcile(ctx, name); err != nil {
			return err
		}
		started := time.Now()
		return vmStartError(instance.StartWithPaths(ctx, inst, false, false, "", guestAgent), inst.Dir, started)
	})
}

func (l *Lima) Stop(ctx context.Context, name string) error {
	inst, err := l.inspect(ctx, name)
	if err != nil {
		return err
	}
	switch {
	case inst.Status == limatype.StatusRunning:
		return withLog(io.Discard, func() error {
			return errors.Join(stopInstance(ctx, inst), reconcile.Reconcile(ctx, ""))
		})
	case fromLima(inst).Orphaned:
		return withLog(io.Discard, func() error {
			if err := stopOrphanedInstance(ctx, inst); err != nil {
				return err
			}
			return reconcile.Reconcile(ctx, "")
		})
	}
	return nil
}

func (l *Lima) Delete(ctx context.Context, name string) error {
	inst, err := l.inspect(ctx, name)
	if err != nil {
		return err
	}
	return withLog(io.Discard, func() error {
		if inst.Status == limatype.StatusRunning {
			if err := stopInstance(ctx, inst); err != nil {
				return err
			}
		} else if fromLima(inst).Orphaned {
			if err := stopOrphanedInstance(ctx, inst); err != nil {
				return err
			}
		}
		return errors.Join(instance.Delete(ctx, inst, true), reconcile.Reconcile(ctx, ""))
	})
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && (isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()))
}

// Shell runs o as `limactl shell` does: over ssh, by the user's login
// shell, from o.Workdir (or fails), with a terminal when stdout is one.
func (l *Lima) Shell(ctx context.Context, name string, o ShellOpts) (int, error) {
	inst, err := l.inspect(ctx, name)
	if err != nil {
		return 0, err
	}
	if inst.Status != limatype.StatusRunning || inst.Config == nil {
		return 0, fmt.Errorf("VM %s is not running", name)
	}
	// Without a workdir, the guest user's home (limactl's fallback).
	cd := "false"
	if o.Workdir != "" {
		cd = "cd " + shellescape.Quote(o.Workdir) + " || exit 1"
	}
	shell := `"$SHELL"`
	if inst.Config.User.Shell != nil {
		shell = shellescape.Quote(*inst.Config.User.Shell)
	}
	script := cd + " ; exec " + shell + " -l"
	if len(o.Args) > 0 {
		q := make([]string, len(o.Args))
		for i, a := range o.Args {
			q[i] = shellescape.Quote(a)
		}
		script += " -c " + shellescape.Quote(strings.Join(q, " "))
	}
	sshExe, err := sshutil.NewSSHExe()
	if err != nil {
		return 0, err
	}
	c := inst.Config
	opts, err := sshutil.SSHOpts(ctx, sshExe, inst.Dir, *c.User.Name, *c.SSH.LoadDotSSHPubKeys,
		*c.SSH.ForwardAgent, *c.SSH.ForwardX11, *c.SSH.ForwardX11Trusted)
	if err != nil {
		return 0, err
	}
	if runtime.GOOS == "windows" {
		// Cygwin-based ssh clients do not multiplex commands (as limactl shell).
		opts = sshutil.SSHOptsRemovingControlPath(opts)
	}
	args := append(append([]string{}, sshExe.Args...), sshutil.SSHArgsFromOpts(opts)...)
	// A terminal when stdout is one, as limactl shell does, or when asked
	// for and stdin is one.
	if isTerminal(o.Stdout) || o.Interactive && isTerminal(o.Stdin) {
		args = append(args, "-t")
	}
	if _, ok := os.LookupEnv("COLORTERM"); ok {
		args = append(args, "-o", "SendEnv=COLORTERM")
	}
	logLevel := "ERROR"
	if sshutil.DetectOpenSSHVersion(ctx, sshExe).LessThan(*semver.New("8.9.0")) {
		logLevel = "QUIET"
	}
	args = append(args, "-o", "LogLevel="+logLevel, "-p", strconv.Itoa(inst.SSHLocalPort), inst.SSHAddress, "--", script)
	cmd := exec.CommandContext(ctx, sshExe.Exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = o.Stdin, o.Stdout, o.Stderr
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}
