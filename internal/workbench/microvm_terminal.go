package workbench

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/sylvinus/agent-vm/internal/microvm"
)

func microVMProjectPath(path string) (id, guest string) {
	id = fmt.Sprintf("project-%x", sha256.Sum256([]byte(path)))[:32]
	return id, "/workspace/" + id
}

func (s *Server) ensureMicroVMShare(path string) error {
	if path == s.environmentDir() {
		return nil
	}
	s.mu.Lock()
	existing := s.projectShares[path]
	s.mu.Unlock()
	if existing != nil {
		return nil
	}
	m := s.microGuest
	client, err := m.client()
	if err != nil {
		return err
	}
	m.mu.Lock()
	shares := m.shares
	m.mu.Unlock()
	id, guest := microVMProjectPath(path)
	if err := shares.Register(microvm.MountSpec{ID: id, LocalPath: path, GuestPath: guest}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.operationContext(), 15*time.Second)
	defer cancel()
	if err := client.Mount(ctx, microvm.MountRequest{ID: id, MountPoint: guest}); err != nil {
		shares.Unregister(id)
		return err
	}
	done := make(chan struct{})
	close(done)
	share := &projectShare{done: done, detach: func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Unmount(ctx, microvm.UnmountRequest{ID: id, MountPoint: guest})
		shares.Unregister(id)
	}}
	s.mu.Lock()
	if s.projectShares == nil {
		s.projectShares = map[string]*projectShare{}
	}
	s.projectShares[path] = share
	s.mu.Unlock()
	return nil
}

func (s *Server) createMicroVMTerminal(input terminalRequest) (*terminalSession, error) {
	if !s.vmRunning() || s.busy() {
		return nil, errTerminalNotReady
	}
	if input.ClaudeAccountID != "" || input.sharedID != "" {
		return nil, errors.New("MicroVM 开发环境暂不支持托管账号会话，请在终端内登录 Claude")
	}
	if input.Kind != "" && input.Kind != "shell" && input.Kind != "claude" {
		return nil, errors.New("MicroVM 支持普通终端和 Claude 终端")
	}
	if err := s.terminals.canStart(); err != nil {
		return nil, err
	}
	workspace, err := s.connectProject(input.Workspace, false)
	if err != nil {
		return nil, err
	}
	workdir := "/home/work"
	if workspace != s.environmentDir() {
		_, workdir = microVMProjectPath(workspace)
	}
	client, err := s.microGuest.client()
	if err != nil {
		return nil, err
	}
	var command []string
	if input.Kind == "claude" {
		s.mu.Lock()
		cmd := s.preferences.ClaudeTerminalCommand
		s.mu.Unlock()
		if strings.TrimSpace(cmd) == "" {
			cmd = "claude"
		}
		command = []string{"/bin/zsh", "-lc", cmd + "; exec /bin/zsh -l"}
	}
	return s.terminals.startSessionWithPTY(nil, workspace, input.Kind, claudeSelection{}, nil, func() (terminalPTY, error) {
		conn, err := client.OpenPTY(s.lifetimeContext(), microvm.PTYOpenRequest{Cols: 100, Rows: 30, Workdir: workdir, Command: command})
		if err != nil {
			return nil, err
		}
		return newMicroVMPTY(conn), nil
	})
}

// Framing remains inside this adapter; terminal subscriptions use their normal
// byte streams and reconnect to the same guest process.
type microVMPTY struct {
	conn    net.Conn
	writeMu sync.Mutex
	pending []byte
	done    chan struct{}
	once    sync.Once
	waitErr error
}

func newMicroVMPTY(conn net.Conn) *microVMPTY {
	return &microVMPTY{conn: conn, done: make(chan struct{})}
}
func (p *microVMPTY) finish(err error) { p.once.Do(func() { p.waitErr = err; close(p.done) }) }
func (p *microVMPTY) Read(b []byte) (int, error) {
	for len(p.pending) == 0 {
		kind, data, err := microvm.ReadPTYFrame(p.conn)
		if err != nil {
			p.finish(err)
			return 0, err
		}
		switch kind {
		case microvm.PTYMsgData:
			p.pending = data
		case microvm.PTYMsgExit:
			code, message, err := microvm.DecodePTYExit(data)
			if err == nil && code != 0 {
				err = fmt.Errorf("guest exited with code %d: %s", code, message)
			}
			p.finish(err)
			return 0, io.EOF
		}
	}
	n := copy(b, p.pending)
	p.pending = p.pending[n:]
	return n, nil
}
func (p *microVMPTY) frame(kind byte, data []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return microvm.WritePTYFrame(p.conn, kind, data)
}
func (p *microVMPTY) Write(b []byte) (int, error) {
	if err := p.frame(microvm.PTYMsgData, b); err != nil {
		return 0, err
	}
	return len(b), nil
}
func (p *microVMPTY) Resize(cols, rows uint16) error {
	return p.frame(microvm.PTYMsgResize, microvm.EncodePTYResize(cols, rows))
}
func (p *microVMPTY) Close() error { err := p.conn.Close(); p.finish(err); return err }
func (p *microVMPTY) Kill() error  { return p.Close() }
func (p *microVMPTY) Wait() error  { <-p.done; return p.waitErr }

func (s *Server) microVMGuestCommand(ctx context.Context, script string) ([]byte, error) {
	client, err := s.microGuest.client()
	if err != nil {
		return nil, err
	}
	conn, err := client.OpenPTY(ctx, microvm.PTYOpenRequest{Cols: 120, Rows: 30, Workdir: "/home/work", Command: []string{"/bin/sh", "-c", "stty -onlcr; exec /bin/bash --noprofile --norc -c " + shellescape.Quote(script)}})
	if err != nil {
		return nil, err
	}
	p := newMicroVMPTY(conn)
	defer p.Close()
	stop := context.AfterFunc(ctx, func() { _ = p.Close() })
	defer stop()
	data, err := io.ReadAll(io.LimitReader(p, 64<<10+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<10 {
		return nil, errors.New("guest response exceeded limit")
	}
	if err = p.Wait(); err != nil {
		return data, err
	}
	return data, nil
}
