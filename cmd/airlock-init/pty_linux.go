//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/sylvinus/agent-vm/internal/microvm"
)

// HandlePTY serves one framed interactive terminal session over PortPTY.
func HandlePTY(ctx context.Context, conn net.Conn, workUser string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	msgType, payload, err := microvm.ReadPTYFrame(conn)
	if err != nil {
		return err
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	if msgType != microvm.PTYMsgOpen {
		return errors.New("expected initial PTYMsgOpen frame")
	}

	var req microvm.PTYOpenRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return err
	}
	if len(req.Command) == 0 {
		req.Command = defaultShellCommand()
	}

	cmd := exec.CommandContext(ctx, req.Command[0], req.Command[1:]...)
	if req.Workdir != "" && filepath.IsAbs(req.Workdir) {
		cmd.Dir = req.Workdir
	}
	cred, homeDir := lookupCredential(workUser)
	if os.Geteuid() == 0 && cred != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
		if cmd.Dir == "" {
			cmd.Dir = homeDir
		}
	}
	cmd.Env = buildPTYEnv(homeDir, workUser, req.Env)

	cols, rows := req.Cols, req.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	defer ptmx.Close()

	var writeMu sync.Mutex
	sendFrame := func(kind byte, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return microvm.WritePTYFrame(conn, kind, data)
	}

	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buf := make([]byte, 16<<10)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				if sendFrame(microvm.PTYMsgData, buf[:n]) != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	go func() {
		for {
			kind, data, rerr := microvm.ReadPTYFrame(conn)
			if rerr != nil {
				cancel()
				_ = ptmx.Close()
				return
			}
			switch kind {
			case microvm.PTYMsgData:
				if _, werr := ptmx.Write(data); werr != nil {
					return
				}
			case microvm.PTYMsgResize:
				if c, r, derr := microvm.DecodePTYResize(data); derr == nil {
					_ = pty.Setsize(ptmx, &pty.Winsize{Cols: c, Rows: r})
				}
			case microvm.PTYMsgSignal:
				if len(data) > 0 && cmd.Process != nil {
					if sig, ok := parseSignalName(string(data)); ok {
						_ = cmd.Process.Signal(sig)
					}
				}
			}
		}
	}()

	waitErr := cmd.Wait()
	select {
	case <-outputDone:
	case <-time.After(time.Second):
		// A descendant can retain the PTY after its shell has exited.
		_ = ptmx.Close()
		<-outputDone
	}
	code := int32(0)
	msg := ""
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code = int32(exitErr.ExitCode())
		} else {
			code = 1
			msg = waitErr.Error()
		}
	}
	_ = sendFrame(microvm.PTYMsgExit, microvm.EncodePTYExit(code, msg))
	return nil
}

func defaultShellCommand() []string {
	for _, sh := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(sh); err == nil {
			return []string{sh, "-l"}
		}
	}
	return []string{"/bin/sh"}
}

func buildPTYEnv(homeDir, workUser string, overrides map[string]string) []string {
	env := map[string]string{
		"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME": homeDir,
		"USER": workUser,
		"TERM": "xterm-256color",
	}
	if raw, err := os.ReadFile("/etc/airlock-env"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k != "" {
				env[k] = v
			}
		}
	}
	blocked := map[string]bool{
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "FTP_PROXY": true, "NO_PROXY": true,
		"http_proxy": true, "https_proxy": true, "all_proxy": true, "ftp_proxy": true, "no_proxy": true,
		"LD_PRELOAD": true, "LD_LIBRARY_PATH": true,
	}
	for k, v := range overrides {
		if !blocked[k] && k != "" {
			env[k] = v
		}
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func parseSignalName(s string) (os.Signal, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "INT", "SIGINT":
		return syscall.SIGINT, true
	case "TERM", "SIGTERM":
		return syscall.SIGTERM, true
	case "HUP", "SIGHUP":
		return syscall.SIGHUP, true
	case "KILL", "SIGKILL":
		return syscall.SIGKILL, true
	default:
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
			return syscall.Signal(n), true
		}
		return nil, false
	}
}

var _ = io.EOF
