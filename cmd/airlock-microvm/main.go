// Command airlock-microvm launches Airlock's lightweight direct-boot MicroVM
// with full 7-layer anti-Claude-location isolation and vsock PTY/SFTP channels.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestprofile"
	"github.com/sylvinus/agent-vm/internal/microvm"
	"golang.org/x/term"
)

func main() {
	stripHostSecretsAndOverrides()
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "Usage: airlock-microvm run --image-dir DIR [--profile DIR] [--project PATH] [--data-disk PATH] [--shell] [--cmd CMD...]")
		os.Exit(2)
	}
	if err := runCmd(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "airlock-microvm: %v\n", err)
		os.Exit(1)
	}
}

func stripHostSecretsAndOverrides() {
	for _, k := range []string{
		"AGENT_VM_UNSAFE_OPEN_NETWORK",
		"AGENT_VM_UNSAFE_WRITABLE_GIT",
		"AGENT_VM_UNSAFE_DISABLE_SECURITY_PROMPTS",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "FTP_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "all_proxy", "ftp_proxy", "no_proxy",
		"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN",
	} {
		_ = os.Unsetenv(k)
	}
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	imageDir := fs.String("image-dir", "", "directory containing vmlinuz, optional initrd.img, rootfs.img, data.img")
	profileDir := fs.String("profile", "", "cage profile directory (default ~/.claude-cage)")
	projectDir := fs.String("project", "", "explicit host project directory to share via confined SFTP")
	dataDisk := fs.String("data-disk", "", "optional override path for writable data.img")
	shellMode := fs.Bool("shell", false, "start an interactive login shell")
	cmdFlag := fs.String("cmd", "", "command string to run inside the guest")
	cpus := fs.Int("cpus", 2, "guest vCPU count")
	memGiB := fs.Int("memory-gib", 2, "guest max memory in GiB")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *imageDir == "" {
		return errors.New("--image-dir is required")
	}

	profile, cfgPath, err := resolveProfile(*profileDir)
	if err != nil {
		return err
	}
	cfg, err := cage.Load(cfgPath)
	if err != nil {
		return err
	}
	if err := cfg.RequireManagedProxy(profile); err != nil {
		return err
	}
	if cfg.GuestProfile == nil {
		gp, err := guestprofile.Create(profile, guestprofile.Options{})
		if err != nil {
			return err
		}
		cfg.GuestProfile = gp
	}

	gate, err := cage.NewEgressGate(cfg)
	if err != nil {
		return err
	}
	defer gate.Close("airlock-microvm exiting")

	kernelPath := filepath.Join(*imageDir, "vmlinuz")
	initrdPath := filepath.Join(*imageDir, "initrd.img")
	if _, err := os.Stat(initrdPath); err != nil {
		initrdPath = ""
	}
	rootfsPath := filepath.Join(*imageDir, "rootfs.img")
	diskPath := *dataDisk
	if diskPath == "" {
		diskPath = filepath.Join(*imageDir, "data.img")
	}

	guestMAC, gatewayMAC, err := loadOrCreateNetIdentity(profile)
	if err != nil {
		return err
	}
	sftpSrv := microvm.NewSFTPServer()
	if *projectDir != "" {
		absProj, err := filepath.Abs(*projectDir)
		if err != nil {
			return err
		}
		if err := sftpSrv.Register(microvm.MountSpec{
			ID:        "project",
			LocalPath: absProj,
			GuestPath: "/workspace",
			ReadOnly:  false,
		}); err != nil {
			return err
		}
	}

	vm, err := microvm.NewVM(microvm.VMConfig{
		KernelPath:          kernelPath,
		InitrdPath:          initrdPath,
		RootfsPath:          rootfsPath,
		DataDiskPath:        diskPath,
		ConsoleLogPath:      filepath.Join(profile, "microvm-console.log"),
		CPUs:                *cpus,
		MemoryBytes:         uint64(*memGiB) << 30,
		BaselineMemoryBytes: 512 << 20,
		GuestMAC:            guestMAC,
		GatewayMAC:          gatewayMAC,
		Gate:                gate,
		SFTP:                sftpSrv,
	})
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := vm.Start(ctx); err != nil {
		return err
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = vm.Stop(stopCtx)
	}()

	client := vm.Client()
	bootCtx, bootCancel := context.WithTimeout(ctx, 20*time.Second)
	defer bootCancel()
	if _, err := client.WaitReady(bootCtx); err != nil {
		return err
	}
	if err := client.Bootstrap(bootCtx, microvm.BootstrapConfig{
		Hostname:    cfg.GuestProfile.Hostname,
		Timezone:    cfg.Timezone,
		Locale:      cfg.Locale,
		Language:    cfg.BrowserLanguage,
		WorkUser:    "work",
		BrowserUser: "browser",
		GuestIP:     microvm.DefaultGuestIP,
		GatewayIP:   microvm.DefaultGatewayIP,
		NetmaskBits: 24,
	}); err != nil {
		return err
	}

	workdir := ""
	if *projectDir != "" {
		if err := client.Mount(bootCtx, microvm.MountRequest{
			ID:         "project",
			MountPoint: "/workspace",
			ReadOnly:   false,
		}); err != nil {
			return err
		}
		workdir = "/workspace"
	}

	canaryFile, err := os.CreateTemp(profile, ".airlock-canary-*")
	if err != nil {
		return err
	}
	canaryPath := canaryFile.Name()
	_ = canaryFile.Close()
	defer os.Remove(canaryPath)

	safetyCtx, safetyCancel := context.WithTimeout(ctx, 45*time.Second)
	report, err := microvm.RunSafetyCheck(safetyCtx, cfg, gate, client, microvm.DefaultExitProbe, canaryPath)
	safetyCancel()
	if err != nil {
		return fmt.Errorf("safety check failed (egress remains closed): %w", err)
	}

	monitorCtx, monitorCancel := context.WithCancel(ctx)
	defer monitorCancel()
	go microvm.MonitorExit(monitorCtx, cfg, gate, report.ID, microvm.DefaultExitProbe, 5*time.Second)

	var command []string
	if !*shellMode {
		if *cmdFlag != "" {
			command = []string{"/bin/sh", "-lc", *cmdFlag}
		} else if rest := fs.Args(); len(rest) > 0 {
			command = rest
		}
	}

	return attachPTY(ctx, client, cfg, workdir, command)
}

func resolveProfile(dir string) (profile, cfgPath string, err error) {
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", "", err
		}
		return abs, filepath.Join(abs, "config.json"), nil
	}
	if envCfg := os.Getenv(cage.ConfigEnv); envCfg != "" {
		abs, err := filepath.Abs(envCfg)
		if err != nil {
			return "", "", err
		}
		return filepath.Dir(abs), abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	profile = filepath.Join(home, ".claude-cage")
	return profile, filepath.Join(profile, "config.json"), nil
}

func loadOrCreateNetIdentity(profile string) (guestMAC, gatewayMAC string, err error) {
	path := filepath.Join(profile, "microvm-net.json")
	var saved struct {
		GuestMAC   string `json:"guest_mac"`
		GatewayMAC string `json:"gateway_mac"`
	}
	if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &saved) == nil && saved.GuestMAC != "" && saved.GatewayMAC != "" {
		return saved.GuestMAC, saved.GatewayMAC, nil
	}
	if saved.GuestMAC, err = microvm.RandomLocalMAC(); err != nil {
		return "", "", err
	}
	if saved.GatewayMAC, err = microvm.RandomLocalMAC(); err != nil {
		return "", "", err
	}
	if data, merr := json.MarshalIndent(saved, "", "  "); merr == nil {
		_ = os.WriteFile(path, append(data, '\n'), 0600)
	}
	return saved.GuestMAC, saved.GatewayMAC, nil
}

func attachPTY(ctx context.Context, client *microvm.GuestClient, cfg cage.Config, workdir string, command []string) error {
	cols, rows := uint16(80), uint16(24)
	fd := int(os.Stdin.Fd())
	isTTY := term.IsTerminal(fd)
	if isTTY {
		if w, h, err := term.GetSize(fd); err == nil && w > 0 && h > 0 {
			cols, rows = uint16(w), uint16(h)
		}
	}

	conn, err := client.OpenPTY(ctx, microvm.PTYOpenRequest{
		Cols:    cols,
		Rows:    rows,
		Workdir: workdir,
		Command: command,
	})
	if err != nil {
		return err
	}
	defer conn.Close()

	if isTTY {
		oldState, err := term.MakeRaw(fd)
		if err == nil {
			defer func() { _ = term.Restore(fd, oldState) }()
		}
	}

	var writeMu sync.Mutex
	sendFrame := func(kind byte, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return microvm.WritePTYFrame(conn, kind, payload)
	}

	if isTTY {
		stopWinch := watchResize(ctx, fd, func(c, r uint16) {
			_ = sendFrame(microvm.PTYMsgResize, microvm.EncodePTYResize(c, r))
		})
		defer stopWinch()
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if sendFrame(microvm.PTYMsgData, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	var oauthTail strings.Builder
	seenOAuth := make(map[string]bool)
	for {
		kind, payload, err := microvm.ReadPTYFrame(conn)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch kind {
		case microvm.PTYMsgData:
			_, _ = os.Stdout.Write(payload)
			if oauthURL := extractClaudeAuthURL(&oauthTail, payload); oauthURL != "" && !seenOAuth[oauthURL] {
				seenOAuth[oauthURL] = true
				go func(target string) {
					bCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
					defer cancel()
					if _, berr := client.StartBrowser(bCtx, microvm.BrowserStartRequest{
						URL:      target,
						Language: cfg.BrowserLanguage,
						Timezone: cfg.Timezone,
					}); berr == nil {
						fmt.Fprintf(os.Stderr, "\r\n[airlock] OAuth login URL opened in isolated guest browser\r\n")
					}
				}(oauthURL)
			}
		case microvm.PTYMsgExit:
			code, msg, derr := microvm.DecodePTYExit(payload)
			if derr != nil {
				return derr
			}
			if code != 0 {
				if msg != "" {
					return fmt.Errorf("guest exited with code %d: %s", code, msg)
				}
				return fmt.Errorf("guest exited with code %d", code)
			}
			return nil
		}
	}
}

var (
	ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	urlFinderRe  = regexp.MustCompile(`https://[^\s"'<>` + "`" + `\x00-\x1f\x7f]+`)
)

func extractClaudeAuthURL(tail *strings.Builder, chunk []byte) string {
	clean := ansiEscapeRe.ReplaceAllString(string(chunk), "")
	tail.WriteString(clean)
	if tail.Len() > 16384 {
		s := tail.String()
		tail.Reset()
		tail.WriteString(s[len(s)-8192:])
	}
	text := strings.ReplaceAll(strings.ReplaceAll(tail.String(), "\r\n", ""), "\n", "")
	matches := urlFinderRe.FindAllString(text, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		cand := strings.TrimRight(matches[i], ".,;:!?)")
		if isClaudeAuthURL(cand) {
			return cand
		}
	}
	return ""
}

func isClaudeAuthURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 8192 || strings.ContainsAny(raw, "\r\n\t \\") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery == "" {
		return false
	}
	p := strings.TrimSuffix(u.EscapedPath(), "/")
	if p != "/oauth/authorize" && p != "/cai/oauth/authorize" {
		return false
	}
	switch strings.ToLower(u.Host) {
	case "claude.ai", "claude.com", "console.anthropic.com", "platform.claude.com", "platform.anthropic.com":
		return true
	}
	return false
}
