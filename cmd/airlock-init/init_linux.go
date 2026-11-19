//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/mdlayher/vsock"
	"github.com/sylvinus/agent-vm/internal/microvm"
	"golang.org/x/sys/unix"
)

var validMountID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func runSFTPBridge(mountID string) error {
	if !validMountID.MatchString(mountID) {
		return fmt.Errorf("invalid mount id %q", mountID)
	}
	conn, err := vsock.Dial(vsock.Host, microvm.PortSFTP, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	hs, err := json.Marshal(microvm.SFTPHandshake{MountID: mountID})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(hs, '\n')); err != nil {
		return err
	}

	errCh := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, os.Stdin)
		_ = conn.CloseWrite()
		errCh <- err
	}()
	go func() {
		_, err := io.Copy(os.Stdout, conn)
		errCh <- err
	}()
	return <-errCh
}

func runInit() error {
	unsetUnsafeEnv()
	if os.Getpid() == 1 {
		if err := setupPID1(); err != nil {
			return err
		}
		return superviseInit()
	}

	ops := newLinuxHostOps()
	_ = ops.ConfigureNetwork(context.Background(), microvm.BootstrapConfig{
		GuestIP:     microvm.DefaultGuestIP,
		GatewayIP:   microvm.DefaultGatewayIP,
		NetmaskBits: 24,
	})

	ctrlLn, err := vsock.ListenContextID(unix.VMADDR_CID_ANY, microvm.PortControl, nil)
	if err != nil {
		return fmt.Errorf("listen vsock:%d: %w", microvm.PortControl, err)
	}
	defer ctrlLn.Close()

	ptyLn, err := vsock.ListenContextID(unix.VMADDR_CID_ANY, microvm.PortPTY, nil)
	if err != nil {
		return fmt.Errorf("listen vsock:%d: %w", microvm.PortPTY, err)
	}
	defer ptyLn.Close()

	browserLn, err := vsock.ListenContextID(unix.VMADDR_CID_ANY, microvm.PortBrowser, nil)
	if err != nil {
		return fmt.Errorf("listen vsock:%d: %w", microvm.PortBrowser, err)
	}
	defer browserLn.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	srv := NewInitServer(ops)
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = ops.StopBrowser(context.Background())
		_ = httpSrv.Close()
		_ = ptyLn.Close()
		_ = browserLn.Close()
	}()

	go func() {
		for {
			conn, err := ptyLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_ = HandlePTY(ctx, c, ops.WorkUser())
			}(conn)
		}
	}()

	go func() {
		for {
			conn, err := browserLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_ = HandleBrowserTunnel(ctx, c, ops.DialLocal)
			}(conn)
		}
	}()

	if err := httpSrv.Serve(ctrlLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func unsetUnsafeEnv() {
	for _, k := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "FTP_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "all_proxy", "ftp_proxy", "no_proxy",
		"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN",
	} {
		_ = os.Unsetenv(k)
	}
}

// PID 1 only supervises/reaps. The service process owns its exec.Cmd waits,
// so a global wait4 cannot consume terminal exit statuses.
func superviseInit() error {
	child := exec.Command("/sbin/airlock-init", "serve")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		return err
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for sig := range signals {
			_ = child.Process.Signal(sig)
		}
	}()
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if pid == child.Process.Pid {
			unix.Sync()
			return unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
		}
	}
}

func setupPID1() error {
	_ = os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	for _, m := range []struct {
		src, dst, fs string
		flags        uintptr
		opts         string
	}{
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"},
		{"tmpfs", "/run", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=0755"},
		{"tmpfs", "/tmp", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
		{"devpts", "/dev/pts", "devpts", unix.MS_NOSUID | unix.MS_NOEXEC, "gid=5,mode=620,ptmxmode=0666"},
		{"tmpfs", "/dev/shm", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
	} {
		_ = os.MkdirAll(m.dst, 0755)
		if err := unix.Mount(m.src, m.dst, m.fs, m.flags, m.opts); err != nil && err != unix.EBUSY {
			return fmt.Errorf("mount %s: %w", m.dst, err)
		}
	}
	for _, module := range []string{"overlay", "fuse", "vmw_vsock_virtio_transport"} {
		if data, err := exec.Command("modprobe", module).CombinedOutput(); err != nil {
			return fmt.Errorf("load %s: %w: %s", module, err, data)
		}
	}
	if err := unix.Mount("/dev/vdb", "/data", "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount data: %w", err)
	}
	// Only configuration and runtime directories need a writable view. The
	// lower image remains immutable; user homes persist on the private data disk.
	for _, name := range []string{"etc", "var", "workspace"} {
		lower, upper, work := "/"+name, "/run/overlay/"+name+"/upper", "/run/overlay/"+name+"/work"
		if err := os.MkdirAll(upper, 0755); err != nil {
			return err
		}
		if err := os.MkdirAll(work, 0755); err != nil {
			return err
		}
		if err := unix.Mount("overlay", lower, "overlay", 0, "lowerdir="+lower+",upperdir="+upper+",workdir="+work); err != nil {
			return fmt.Errorf("overlay %s: %w", name, err)
		}
	}
	if _, err := os.Stat("/data/home"); os.IsNotExist(err) {
		if data, err := exec.Command("cp", "-a", "/home", "/data/home").CombinedOutput(); err != nil {
			return fmt.Errorf("prepare homes: %w: %s", err, data)
		}
	}
	return unix.Mount("/data/home", "/home", "", unix.MS_BIND, "")
}

type linuxHostOps struct {
	mu          sync.Mutex
	browserOp   sync.Mutex
	workUser    string
	browserUser string
	tz          string
	locale      string
	lang        string
	mounts      map[string]string
	vncCmd      *exec.Cmd
	wmCmd       *exec.Cmd
	chromeCmd   *exec.Cmd
	browserURL  string
}

func newLinuxHostOps() *linuxHostOps {
	return &linuxHostOps{
		workUser:    "work",
		browserUser: "browser",
		tz:          "Etc/UTC",
		locale:      "en_US.UTF-8",
		lang:        "en-US",
		mounts:      make(map[string]string),
	}
}

func (o *linuxHostOps) WorkUser() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.workUser
}

func (o *linuxHostOps) ConfigureNetwork(ctx context.Context, cfg microvm.BootstrapConfig) error {
	guestIP := cfg.GuestIP
	if guestIP == "" {
		guestIP = microvm.DefaultGuestIP
	}
	gatewayIP := cfg.GatewayIP
	if gatewayIP == "" {
		gatewayIP = microvm.DefaultGatewayIP
	}
	bits := cfg.NetmaskBits
	if bits <= 0 || bits > 32 {
		bits = 24
	}
	for _, args := range [][]string{
		{"link", "set", "lo", "up"},
		{"link", "set", "eth0", "up"},
		{"addr", "replace", fmt.Sprintf("%s/%d", guestIP, bits), "dev", "eth0"},
		{"route", "replace", "default", "via", gatewayIP, "dev", "eth0"},
	} {
		cmd := exec.CommandContext(ctx, "ip", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ip %v: %w (%s)", args, err, strings.TrimSpace(string(out)))
		}
	}
	_ = exec.CommandContext(ctx, "ip", "-6", "addr", "flush", "dev", "eth0").Run()
	return os.WriteFile("/etc/resolv.conf", []byte("nameserver "+gatewayIP+"\n"), 0644)
}

func (o *linuxHostOps) ApplyBootstrap(_ context.Context, cfg microvm.BootstrapConfig) error {
	if cfg.Hostname != "" {
		if err := unix.Sethostname([]byte(cfg.Hostname)); err != nil {
			return err
		}
	}
	if cfg.Timezone != "" {
		zonePath := filepath.Join("/usr/share/zoneinfo", filepath.Clean("/"+cfg.Timezone))
		if _, err := os.Stat(zonePath); err == nil {
			_ = os.Remove("/etc/localtime")
			if err := os.Symlink(zonePath, "/etc/localtime"); err != nil {
				return err
			}
		}
		_ = os.WriteFile("/etc/timezone", []byte(cfg.Timezone+"\n"), 0644)
		_ = os.Setenv("TZ", cfg.Timezone)
	}
	if cfg.Locale != "" {
		_ = os.Setenv("LANG", cfg.Locale)
		_ = os.Setenv("LC_ALL", cfg.Locale)
	}

	o.mu.Lock()
	if cfg.WorkUser != "" {
		o.workUser = cfg.WorkUser
	}
	if cfg.BrowserUser != "" {
		o.browserUser = cfg.BrowserUser
	}
	if cfg.Timezone != "" {
		o.tz = cfg.Timezone
	}
	if cfg.Locale != "" {
		o.locale = cfg.Locale
	}
	if cfg.Language != "" {
		o.lang = cfg.Language
	}
	workUser, browserUser := o.workUser, o.browserUser
	tz, locale, lang := o.tz, o.locale, o.lang
	o.mu.Unlock()

	envContent := fmt.Sprintf("TZ=%s\nLANG=%s\nLC_ALL=%s\nCAGE_LANGUAGE=%s\n", tz, locale, locale, lang)
	if err := os.WriteFile("/etc/airlock-env", []byte(envContent), 0644); err != nil {
		return err
	}
	ensureDataDirs(workUser, browserUser)
	return nil
}

func ensureDataDirs(workUser, browserUser string) {
	_ = os.MkdirAll("/data/browser-profile", 0700)
	_ = os.MkdirAll("/data/claude", 0700)
	if u, err := user.Lookup(workUser); err == nil {
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		_ = os.Chown("/data/claude", uid, gid)
		// Avoid the first-run zsh wizard consuming terminal input. Never replace
		// a user's existing startup file, including an intentionally empty one.
		if f, err := os.OpenFile(filepath.Join(u.HomeDir, ".zshrc"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644); err == nil {
			_ = f.Chown(uid, gid)
			_ = f.Close()
		}
		claudeLink := filepath.Join(u.HomeDir, ".claude")
		if _, err := os.Lstat(claudeLink); os.IsNotExist(err) {
			_ = os.MkdirAll(u.HomeDir, 0755)
			_ = os.Chown(u.HomeDir, uid, gid)
			_ = os.Symlink("/data/claude", claudeLink)
		}
	}
	if bu, err := user.Lookup(browserUser); err == nil {
		uid, _ := strconv.Atoi(bu.Uid)
		gid, _ := strconv.Atoi(bu.Gid)
		_ = os.Chown("/data/browser-profile", uid, gid)
		_ = os.MkdirAll(bu.HomeDir, 0755)
		_ = os.Chown(bu.HomeDir, uid, gid)
	}
}

func (o *linuxHostOps) ReadMemInfo() (total, avail uint64) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	return total, avail
}

func (o *linuxHostOps) MountSFTP(ctx context.Context, req microvm.MountRequest) error {
	if !validMountID.MatchString(req.ID) {
		return fmt.Errorf("invalid mount id %q", req.ID)
	}
	if !filepath.IsAbs(req.MountPoint) || strings.Contains(req.MountPoint, "..") {
		return fmt.Errorf("invalid mount point %q", req.MountPoint)
	}
	mp := filepath.Clean(req.MountPoint)
	if err := os.MkdirAll(mp, 0755); err != nil {
		return err
	}
	opts := fmt.Sprintf("ssh_command=/sbin/airlock-init sftp-bridge %s,allow_other,cache=no,uid=1000,gid=1000", req.ID)
	if req.ReadOnly {
		opts += ",ro"
	}
	cmd := exec.CommandContext(ctx, "sshfs", "airlock:/", mp, "-o", opts)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sshfs mount failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	o.mu.Lock()
	o.mounts[req.ID] = mp
	o.mu.Unlock()
	return nil
}

func (o *linuxHostOps) UnmountPath(_ context.Context, req microvm.UnmountRequest) error {
	mp := req.MountPoint
	o.mu.Lock()
	if mp == "" && req.ID != "" {
		mp = o.mounts[req.ID]
	}
	if req.ID != "" {
		delete(o.mounts, req.ID)
	}
	o.mu.Unlock()
	if mp == "" {
		return errors.New("missing mount_point or unknown mount id")
	}
	if err := unix.Unmount(mp, 0); err != nil {
		return unix.Unmount(mp, unix.MNT_DETACH)
	}
	return nil
}

func (o *linuxHostOps) StartBrowser(ctx context.Context, req microvm.BrowserStartRequest) (microvm.BrowserStatusResponse, error) {
	o.browserOp.Lock()
	defer o.browserOp.Unlock()
	o.mu.Lock()
	if o.chromeCmd != nil && o.chromeCmd.Process != nil {
		st := o.browserStatusLocked()
		o.mu.Unlock()
		return st, nil
	}
	browserUser := o.browserUser
	tz := req.Timezone
	if tz == "" {
		tz = o.tz
	}
	lang := req.Language
	if lang == "" {
		lang = o.lang
	}
	locale := o.locale
	o.mu.Unlock()

	width, height := req.Width, req.Height
	if width <= 0 || height <= 0 {
		width, height = 1440, 900
	}
	targetURL := req.URL
	if targetURL == "" {
		targetURL = "about:blank"
	}

	cred, homeDir := lookupCredential(browserUser)
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + homeDir,
		"DISPLAY=:77",
		"TZ=" + tz,
		"LANG=" + locale,
		"LC_ALL=" + locale,
	}

	vnc := exec.Command("Xtigervnc", ":77", "-geometry", fmt.Sprintf("%dx%d", width, height), "-depth", "24", "-rfbport", "15900", "-localhost", "-nolisten", "tcp", "-SecurityTypes", "None", "-AlwaysShared")
	vnc.Env = env
	vnc.Stdout, vnc.Stderr = os.Stdout, os.Stderr
	if cred != nil {
		vnc.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	}
	if err := vnc.Start(); err != nil {
		return microvm.BrowserStatusResponse{}, fmt.Errorf("start Xtigervnc: %w", err)
	}
	go func() { _ = vnc.Wait() }()
	if !waitTCPReady(ctx, "127.0.0.1:15900", 3*time.Second) {
		_ = vnc.Process.Kill()
		return microvm.BrowserStatusResponse{}, errors.New("Xtigervnc did not open port 15900")
	}

	wm := exec.Command("openbox")
	wm.Env = env
	wm.Stdout, wm.Stderr = os.Stdout, os.Stderr
	if cred != nil {
		wm.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	}
	if err := wm.Start(); err != nil {
		_ = vnc.Process.Kill()
		return microvm.BrowserStatusResponse{}, fmt.Errorf("start openbox: %w", err)
	}
	go func() { _ = wm.Wait() }()

	chromeBin := findChromiumBinary()
	chrome := exec.Command(chromeBin,
		"--user-data-dir=/data/browser-profile",
		"--remote-debugging-port=9222",
		"--disable-quic",
		"--disable-blink-features=AutomationControlled",
		"--no-first-run",
		"--no-default-browser-check",
		"--start-fullscreen",
		fmt.Sprintf("--window-size=%d,%d", width, height),
		"--lang="+lang,
		"--accept-lang="+lang,
		targetURL,
	)
	chrome.Env = env
	chrome.Stdout, chrome.Stderr = os.Stdout, os.Stderr
	if cred != nil {
		chrome.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	}
	if err := chrome.Start(); err != nil {
		_ = wm.Process.Kill()
		_ = vnc.Process.Kill()
		return microvm.BrowserStatusResponse{}, fmt.Errorf("start chromium: %w", err)
	}
	if !waitTCPReady(ctx, "127.0.0.1:9222", 8*time.Second) {
		_ = chrome.Process.Kill()
		_ = chrome.Wait()
		_ = wm.Process.Kill()
		_ = vnc.Process.Kill()
		return microvm.BrowserStatusResponse{}, errors.New("Chromium did not open its control port")
	}

	o.mu.Lock()
	o.vncCmd = vnc
	o.wmCmd = wm
	o.chromeCmd = chrome
	o.browserURL = targetURL
	st := o.browserStatusLocked()
	o.mu.Unlock()
	go func() {
		_ = chrome.Wait()
		o.mu.Lock()
		if o.chromeCmd == chrome {
			o.chromeCmd = nil
		}
		o.mu.Unlock()
		_ = wm.Process.Kill()
		_ = vnc.Process.Kill()
	}()
	return st, nil
}

func (o *linuxHostOps) StopBrowser(_ context.Context) error {
	o.browserOp.Lock()
	defer o.browserOp.Unlock()
	o.mu.Lock()
	chrome := o.chromeCmd
	wm := o.wmCmd
	vnc := o.vncCmd
	o.chromeCmd, o.wmCmd, o.vncCmd, o.browserURL = nil, nil, nil, ""
	o.mu.Unlock()

	if chrome != nil && chrome.Process != nil {
		_ = chrome.Process.Signal(syscall.SIGTERM)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if chrome.Process.Signal(syscall.Signal(0)) != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		_ = chrome.Process.Kill()
	}
	for _, cmd := range []*exec.Cmd{wm, vnc} {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			time.Sleep(100 * time.Millisecond)
			_ = cmd.Process.Kill()
		}
	}
	return nil
}

func (o *linuxHostOps) BrowserStatus() microvm.BrowserStatusResponse {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.browserStatusLocked()
}

func (o *linuxHostOps) browserStatusLocked() microvm.BrowserStatusResponse {
	if o.chromeCmd == nil || o.chromeCmd.Process == nil {
		return microvm.BrowserStatusResponse{}
	}
	if o.chromeCmd.Process.Signal(syscall.Signal(0)) != nil {
		o.chromeCmd = nil
		return microvm.BrowserStatusResponse{}
	}
	return microvm.BrowserStatusResponse{
		Running:  true,
		VNCReady: tcpReachable("127.0.0.1:15900"),
		CDPReady: tcpReachable("127.0.0.1:9222"),
		PID:      o.chromeCmd.Process.Pid,
		URL:      o.browserURL,
	}
}

func (o *linuxHostOps) CheckIPv6Routes() (defaultRoutes, globalAddrs int) {
	if raw, err := os.ReadFile("/proc/net/ipv6_route"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 10 && fields[0] == "00000000000000000000000000000000" && fields[1] == "00" {
				defaultRoutes++
			}
		}
	}
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			addrs, _ := iface.Addrs()
			for _, addr := range addrs {
				if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() == nil && ipNet.IP.IsGlobalUnicast() {
					globalAddrs++
				}
			}
		}
	}
	return defaultRoutes, globalAddrs
}

func (o *linuxHostOps) CheckPrivateFile(path string) string {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) || os.IsPermission(err) {
			return "unreachable"
		}
		return "unknown"
	}
	_ = f.Close()
	return "readable"
}

func (o *linuxHostOps) CheckSharedFile(path, challenge string) bool {
	data, err := os.ReadFile(path)
	if err != nil || string(data) != challenge {
		return false
	}
	return os.WriteFile(path, []byte(challenge+":guest"), 0644) == nil
}

func (o *linuxHostOps) DialLocal(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, addr)
}

func (o *linuxHostOps) DialExternal(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 1200 * time.Millisecond}).DialContext(ctx, network, addr)
}

func (o *linuxHostOps) SendUDP(addr string, payload []byte) bool {
	conn, err := net.DialTimeout("udp4", addr, time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_, err = conn.Write(payload)
	return err == nil
}

func (o *linuxHostOps) LookupHost(ctx context.Context, name string) bool {
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(lookupCtx, name)
	return err == nil && len(addrs) > 0
}

func (o *linuxHostOps) FetchDiagnostic(ctx context.Context, path string) microvm.NetworkProbeResult {
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 12 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://198.18.0.254"+path, nil)
	if err != nil {
		return microvm.NetworkProbeResult{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return microvm.NetworkProbeResult{}
	}
	defer resp.Body.Close()
	var body struct {
		IP string `json:"ip"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
	return microvm.NetworkProbeResult{Status: resp.StatusCode, IP: body.IP}
}

func (o *linuxHostOps) ReadResolvConf() ([]string, bool) {
	raw, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil, false
	}
	var servers []string
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			servers = append(servers, strings.Split(fields[1], "%")[0])
		}
	}
	return servers, true
}

func (o *linuxHostOps) ReadTimezoneEnv(expectedTZ string) TZObservation {
	o.mu.Lock()
	tz, lang, lcAll := os.Getenv("TZ"), os.Getenv("LANG"), os.Getenv("LC_ALL")
	if tz == "" {
		tz = o.tz
	}
	if lang == "" {
		lang = o.locale
	}
	if lcAll == "" {
		lcAll = o.locale
	}
	o.mu.Unlock()

	now := time.Now()
	_, actual := now.Zone()
	obs := TZObservation{
		TZ:              tz,
		Lang:            lang,
		LCAll:           lcAll,
		LocaleValid:     lang != "" && lcAll != "",
		UTCOffsetActual: &actual,
	}
	if raw, err := os.ReadFile("/etc/timezone"); err == nil {
		obs.SystemTimezone = strings.TrimSpace(string(raw))
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		obs.LocaltimeTarget = target
	}
	if tzRaw, err := os.ReadFile("/etc/localtime"); err == nil {
		if loc, err := time.LoadLocationFromTZData("localtime", tzRaw); err == nil {
			_, off := now.In(loc).Zone()
			obs.UTCOffsetSystem = &off
		}
	}
	if expectedTZ != "" {
		if loc, err := time.LoadLocation(expectedTZ); err == nil {
			_, off := now.In(loc).Zone()
			obs.UTCOffsetExpected = &off
		}
	}
	return obs
}

func lookupCredential(username string) (*syscall.Credential, string) {
	u, err := user.Lookup(username)
	if err != nil {
		return nil, "/tmp"
	}
	uid, err1 := strconv.ParseUint(u.Uid, 10, 32)
	gid, err2 := strconv.ParseUint(u.Gid, 10, 32)
	if err1 != nil || err2 != nil {
		return nil, u.HomeDir
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, u.HomeDir
}

func findChromiumBinary() string {
	for _, cand := range []string{"/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome"} {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "chromium"
}

func waitTCPReady(ctx context.Context, addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		if tcpReachable(addr) {
			return true
		}
		time.Sleep(40 * time.Millisecond)
	}
	return false
}

func tcpReachable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
