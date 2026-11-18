package herdrbridge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const OfficialInstallScriptURL = "https://herdr.dev/install.sh"

// HostInstallStatus describes whether `herdr` / `hc` is installed and running on the host.
type HostInstallStatus struct {
	Installed         bool   `json:"installed"`
	Backend           string `json:"backend,omitempty"`
	BinaryPath        string `json:"binary_path,omitempty"`
	Version           string `json:"version,omitempty"`
	ServerRunning     bool   `json:"server_running"`
	SocketPath        string `json:"socket_path,omitempty"`
	BrewAvailable     bool   `json:"brew_available"`
	BrewPath          string `json:"brew_path,omitempty"`
	RecommendedMethod string `json:"recommended_method,omitempty"`
	Error             string `json:"error,omitempty"`
}

func resolveBrewBinary() string {
	for _, candidate := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	if resolved, err := exec.LookPath("brew"); err == nil {
		return resolved
	}
	return ""
}

// DetectHost inspects the host for an installed `herdr` (or `hc`) binary, daemon state, and Homebrew availability.
func DetectHost(ctx context.Context, cfg Config) HostInstallStatus {
	brewPath := resolveBrewBinary()
	status := HostInstallStatus{
		BrewAvailable: brewPath != "",
		BrewPath:      brewPath,
	}
	if brewPath != "" {
		status.RecommendedMethod = "brew"
	} else {
		status.RecommendedMethod = "script"
	}

	if cfg.HCPath == "" {
		if bin, err := resolveHerdrBinary(cfg.HerdrPath); err == nil && bin != "" {
			r := NewHerdrRunner(bin, cfg.SocketPath, cfg.Session)
			health := r.Health(ctx)
			status.Installed = true
			status.Backend = "herdr"
			status.BinaryPath = bin
			status.Version = health.Version
			status.ServerRunning = health.Available
			status.SocketPath = health.SocketPath
			if !health.Available {
				status.Error = health.Error
			}
			return status
		}
	}

	if bin, err := resolveNamedBinary(cfg.HCPath, "hc"); err == nil && bin != "" {
		r := NewExecRunner(bin, cfg.SocketPath, cfg.Session)
		health := r.Health(ctx)
		status.Installed = true
		status.Backend = "hc"
		status.BinaryPath = bin
		status.ServerRunning = health.Available
		if !health.Available {
			status.Error = health.Error
		}
		return status
	}

	status.Error = "未检测到宿主机 Herdr (`herdr` 或 `hc`)"
	return status
}

// InstallHost installs or upgrades Herdr on the host using Homebrew or the official install script.
func InstallHost(ctx context.Context, cfg Config, method string, startServer bool, logf func(string)) (HostInstallStatus, error) {
	if runtime.GOOS == "windows" {
		return DetectHost(ctx, cfg), errors.New("Windows 宿主机暂不支持一键安装 Herdr")
	}
	method = strings.TrimSpace(strings.ToLower(method))
	if method == "" {
		method = "auto"
	}
	if method != "auto" && method != "brew" && method != "script" {
		return DetectHost(ctx, cfg), fmt.Errorf("不支持的安装方式：%s", method)
	}

	before := DetectHost(ctx, cfg)
	if method == "auto" {
		if before.BrewAvailable {
			method = "brew"
		} else {
			method = "script"
		}
	}

	switch method {
	case "brew":
		if !before.BrewAvailable {
			return before, errors.New("未找到 Homebrew (`brew`)，请先安装 Homebrew 或选择官方脚本安装")
		}
		if err := installWithBrew(ctx, before.BrewPath, logf); err != nil {
			return DetectHost(ctx, cfg), err
		}
	case "script":
		if err := installWithOfficialScript(ctx, logf); err != nil {
			return DetectHost(ctx, cfg), err
		}
	}

	if startServer {
		return StartHostServer(ctx, cfg, logf)
	}
	return DetectHost(ctx, cfg), nil
}

// StartHostServer ensures the host Herdr daemon is running.
func StartHostServer(ctx context.Context, cfg Config, logf func(string)) (HostInstallStatus, error) {
	st := DetectHost(ctx, cfg)
	if !st.Installed {
		return st, errors.New("宿主机尚未安装 Herdr，请先执行安装")
	}
	if st.ServerRunning {
		emitLog(logf, "Herdr 后台服务已在运行")
		return st, nil
	}
	if st.Backend != "herdr" {
		return st, errors.New("当前检测到独立 `hc` 二进制，请手动确认 `herdr-core` 服务状态")
	}

	if st.BrewAvailable {
		check := exec.CommandContext(ctx, st.BrewPath, "list", "--versions", "herdr")
		if check.Run() == nil {
			emitLog(logf, "正在通过 brew services 启动 Herdr 后台服务…")
			if err := runLoggedCommand(ctx, "", nil, logf, st.BrewPath, "services", "start", "herdr"); err == nil {
				if ready, ok := waitForHostServer(ctx, cfg, 8*time.Second); ok {
					return ready, nil
				}
			}
		}
	}

	emitLog(logf, "正在以无头服务模式启动 `herdr server`…")
	if err := spawnDetachedHerdrServer(st.BinaryPath, cfg); err != nil {
		return DetectHost(ctx, cfg), err
	}
	if ready, ok := waitForHostServer(ctx, cfg, 8*time.Second); ok {
		return ready, nil
	}
	latest := DetectHost(ctx, cfg)
	if latest.Error != "" {
		return latest, errors.New("Herdr 服务启动超时：" + latest.Error)
	}
	return latest, errors.New("Herdr 服务启动超时")
}

func waitForHostServer(ctx context.Context, cfg Config, timeout time.Duration) (HostInstallStatus, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st := DetectHost(ctx, cfg)
		if st.ServerRunning {
			return st, true
		}
		select {
		case <-ctx.Done():
			return st, false
		case <-time.After(350 * time.Millisecond):
		}
	}
	st := DetectHost(ctx, cfg)
	return st, st.ServerRunning
}

func installWithBrew(ctx context.Context, brewPath string, logf func(string)) error {
	env := setEnv(os.Environ(), "HOMEBREW_NO_ENV_HINTS", "1")
	check := exec.CommandContext(ctx, brewPath, "list", "--versions", "herdr")
	if check.Run() == nil {
		emitLog(logf, "检测到 Homebrew 已安装 herdr，正在检查更新…")
		return runLoggedCommand(ctx, "", env, logf, brewPath, "upgrade", "herdr")
	}
	emitLog(logf, "正在执行 `brew install herdr`…")
	return runLoggedCommand(ctx, "", env, logf, brewPath, "install", "herdr")
}

func installWithOfficialScript(ctx context.Context, logf func(string)) error {
	emitLog(logf, "正在从官方地址下载 Herdr 安装脚本…")
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, OfficialInstallScriptURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载官方安装脚本失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载官方安装脚本返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return fmt.Errorf("读取安装脚本失败：%w", err)
	}
	if len(body) == 0 || len(body) > 1<<20 {
		return errors.New("官方安装脚本大小异常")
	}
	if !strings.HasPrefix(string(body), "#!") {
		return errors.New("官方安装脚本格式无效")
	}

	tmp, err := os.CreateTemp("", "cage-herdr-install-*.sh")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o700); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	emitLog(logf, "正在执行官方安装脚本…")
	home, _ := os.UserHomeDir()
	env := os.Environ()
	if home != "" {
		localBin := filepath.Join(home, ".local", "bin")
		_ = os.MkdirAll(localBin, 0o755)
		env = setEnv(env, "PATH", localBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return runLoggedCommand(ctx, "", env, logf, "/bin/sh", tmpPath)
}

func spawnDetachedHerdrServer(binPath string, cfg Config) error {
	args := []string{}
	if cfg.Session != "" {
		args = append(args, "--session", cfg.Session)
	}
	args = append(args, "server")
	cmd := exec.Command(binPath, args...)
	env := os.Environ()
	if cfg.SocketPath != "" {
		env = setEnv(env, "HERDR_SOCKET_PATH", cfg.SocketPath)
	}
	cmd.Env = env
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		defer devNull.Close()
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 herdr server 失败：%w", err)
	}
	return cmd.Process.Release()
}

func runLoggedCommand(ctx context.Context, dir string, env []string, logf func(string), name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return err
	}
	_ = pw.Close()

	var tail []string
	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 16*1024), 256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		emitLog(logf, line)
		tail = append(tail, line)
		if len(tail) > 12 {
			tail = tail[1:]
		}
	}
	_ = pr.Close()
	if err := cmd.Wait(); err != nil {
		if len(tail) > 0 {
			return fmt.Errorf("%s 失败：%w (%s)", filepath.Base(name), err, strings.Join(tail, " | "))
		}
		return fmt.Errorf("%s 失败：%w", filepath.Base(name), err)
	}
	return nil
}

func emitLog(logf func(string), msg string) {
	if logf != nil && strings.TrimSpace(msg) != "" {
		logf(msg)
	}
}
