package herdrbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RunOptions configures launching a background command or agent in Herdr.
type RunOptions struct {
	PaneName    string
	Workdir     string
	Command     string
	HerdrKind   string
	Interactive bool
	Args        []string
}

// PaneRef identifies a target terminal or agent across both `herdr` and `hc`.
type PaneRef struct {
	Name    string
	PaneID  string
	IsAgent bool
}

// PaneInfo summarizes a single Herdr terminal/agent returned by list commands.
type PaneInfo struct {
	ID              string `json:"id,omitempty"`
	PaneID          string `json:"pane_id,omitempty"`
	Name            string `json:"name,omitempty"`
	Status          string `json:"status,omitempty"`
	State           string `json:"state,omitempty"`
	AgentStatus     string `json:"agent_status,omitempty"`
	Running         *bool  `json:"running,omitempty"`
	Exited          *bool  `json:"exited,omitempty"`
	Blocked         *bool  `json:"blocked,omitempty"`
	WaitingApproval *bool  `json:"waiting_approval,omitempty"`
	ExitCode        *int   `json:"exit_code,omitempty"`
}

// HealthInfo reports whether `herdr` / `hc` and its daemon are reachable.
type HealthInfo struct {
	Available  bool   `json:"available"`
	Backend    string `json:"backend,omitempty"`
	BinaryPath string `json:"binary_path,omitempty"`
	HCPath     string `json:"hc_path,omitempty"`
	Version    string `json:"version,omitempty"`
	SocketPath string `json:"socket_path,omitempty"`
	Error      string `json:"error,omitempty"`
}

// WaitResult captures the outcome of waiting on a pane or agent turn.
type WaitResult struct {
	Completed bool
	TimedOut  bool
	Blocked   bool
	Output    string
}

// Runner abstracts Herdr CLI operations (`herdr` 0.9+ or standalone `hc`).
type Runner interface {
	Health(ctx context.Context) HealthInfo
	RunBackground(ctx context.Context, opts RunOptions) (paneID string, isAgent bool, err error)
	Send(ctx context.Context, ref PaneRef, text string) error
	PromptAndWait(ctx context.Context, ref PaneRef, text string, timeout time.Duration) (WaitResult, error)
	Wait(ctx context.Context, ref PaneRef, timeout time.Duration) (WaitResult, error)
	Read(ctx context.Context, ref PaneRef, lines int) (string, error)
	List(ctx context.Context) ([]PaneInfo, error)
	Kill(ctx context.Context, ref PaneRef) error
	Remove(ctx context.Context, ref PaneRef) error
}

// ExecRunner invokes standalone `hc` (`herdr-core`) without shell interpretation.
type ExecRunner struct {
	HCPath     string
	SocketPath string
	Session    string
}

func NewExecRunner(hcPath, socketPath, session string) *ExecRunner {
	return &ExecRunner{
		HCPath:     strings.TrimSpace(hcPath),
		SocketPath: strings.TrimSpace(socketPath),
		Session:    strings.TrimSpace(session),
	}
}

// NewAutoRunner prefers the full `herdr` CLI when installed (or configured via
// HerdrPath), falling back to standalone `hc` (`herdr-core`).
func NewAutoRunner(cfg Config) Runner {
	if cfg.HerdrPath != "" {
		return NewHerdrRunner(cfg.HerdrPath, cfg.SocketPath, cfg.Session)
	}
	if cfg.HCPath != "" {
		return NewExecRunner(cfg.HCPath, cfg.SocketPath, cfg.Session)
	}
	return &AutoRunner{cfg: cfg}
}

// AutoRunner dynamically resolves `herdr` or `hc` on each call so installing
// Herdr from Cage Settings works immediately without restarting the workbench.
type AutoRunner struct {
	cfg Config
}

func (a *AutoRunner) delegate() Runner {
	if bin, err := resolveHerdrBinary(a.cfg.HerdrPath); err == nil && bin != "" {
		return NewHerdrRunner(bin, a.cfg.SocketPath, a.cfg.Session)
	}
	return NewExecRunner(a.cfg.HCPath, a.cfg.SocketPath, a.cfg.Session)
}

func (a *AutoRunner) Health(ctx context.Context) HealthInfo {
	return a.delegate().Health(ctx)
}

func (a *AutoRunner) RunBackground(ctx context.Context, opts RunOptions) (string, bool, error) {
	return a.delegate().RunBackground(ctx, opts)
}

func (a *AutoRunner) Send(ctx context.Context, ref PaneRef, text string) error {
	return a.delegate().Send(ctx, ref, text)
}

func (a *AutoRunner) PromptAndWait(ctx context.Context, ref PaneRef, text string, timeout time.Duration) (WaitResult, error) {
	return a.delegate().PromptAndWait(ctx, ref, text, timeout)
}

func (a *AutoRunner) Wait(ctx context.Context, ref PaneRef, timeout time.Duration) (WaitResult, error) {
	return a.delegate().Wait(ctx, ref, timeout)
}

func (a *AutoRunner) Read(ctx context.Context, ref PaneRef, lines int) (string, error) {
	return a.delegate().Read(ctx, ref, lines)
}

func (a *AutoRunner) List(ctx context.Context) ([]PaneInfo, error) {
	return a.delegate().List(ctx)
}

func (a *AutoRunner) Kill(ctx context.Context, ref PaneRef) error {
	return a.delegate().Kill(ctx, ref)
}

func (a *AutoRunner) Remove(ctx context.Context, ref PaneRef) error {
	return a.delegate().Remove(ctx, ref)
}

func resolveNamedBinary(configured, name string) (string, error) {
	if configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		if resolved, err := exec.LookPath(configured); err == nil {
			return resolved, nil
		}
		return "", fmt.Errorf("configured %s binary not found: %s", name, configured)
	}
	home, _ := os.UserHomeDir()
	var candidates []string
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", name),
			filepath.Join(home, ".cargo", "bin", name),
		)
	}
	candidates = append(candidates, "/opt/homebrew/bin/"+name, "/usr/local/bin/"+name)
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if resolved, err := exec.LookPath(name); err == nil {
		return resolved, nil
	}
	return "", fmt.Errorf("%s binary not found in PATH or standard locations", name)
}

func (r *ExecRunner) resolveBinary() (string, error) {
	return resolveNamedBinary(r.HCPath, "hc")
}

func (r *ExecRunner) commandEnv() []string {
	env := os.Environ()
	if r.SocketPath != "" {
		env = setEnv(env, "HERDR_SOCKET_PATH", r.SocketPath)
	}
	if r.Session != "" {
		env = setEnv(env, "HC_SESSION", r.Session)
	}
	return env
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}

func (r *ExecRunner) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	bin, err := r.resolveBinary()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = r.commandEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("hc %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return stdout.Bytes(), fmt.Errorf("hc %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

func (r *ExecRunner) Health(ctx context.Context) HealthInfo {
	bin, err := r.resolveBinary()
	if err != nil {
		return HealthInfo{Available: false, Backend: "hc", Error: err.Error()}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := r.run(probeCtx, "", "ls", "--json"); err != nil {
		return HealthInfo{Available: false, Backend: "hc", BinaryPath: bin, HCPath: bin, Error: err.Error()}
	}
	return HealthInfo{Available: true, Backend: "hc", BinaryPath: bin, HCPath: bin}
}

func (r *ExecRunner) RunBackground(ctx context.Context, opts RunOptions) (string, bool, error) {
	args := []string{"run", "--bg", "--name", opts.PaneName, "--", opts.Command}
	args = append(args, opts.Args...)
	_, err := r.run(ctx, opts.Workdir, args...)
	return opts.PaneName, false, err
}

func (r *ExecRunner) Send(ctx context.Context, ref PaneRef, text string) error {
	_, err := r.run(ctx, "", "send", ref.Name, text)
	return err
}

func (r *ExecRunner) PromptAndWait(ctx context.Context, ref PaneRef, text string, timeout time.Duration) (WaitResult, error) {
	if err := r.Send(ctx, ref, text); err != nil {
		return WaitResult{}, err
	}
	return r.Wait(ctx, ref, timeout)
}

func (r *ExecRunner) Wait(ctx context.Context, ref PaneRef, timeout time.Duration) (WaitResult, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := r.run(waitCtx, "", "wait", ref.Name)
	if err != nil {
		if errors.Is(waitCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return WaitResult{TimedOut: true, Output: StripANSI(string(out))}, nil
		}
		return WaitResult{Output: StripANSI(string(out))}, err
	}
	return WaitResult{Completed: true, Output: StripANSI(string(out))}, nil
}

func (r *ExecRunner) Read(ctx context.Context, ref PaneRef, lines int) (string, error) {
	args := []string{"read"}
	if lines > 0 {
		args = append(args, "--lines", strconv.Itoa(lines))
	}
	args = append(args, ref.Name)
	out, err := r.run(ctx, "", args...)
	if err != nil {
		return "", err
	}
	return StripANSI(string(out)), nil
}

func (r *ExecRunner) List(ctx context.Context) ([]PaneInfo, error) {
	out, err := r.run(ctx, "", "ls", "--json")
	if err != nil {
		return nil, err
	}
	return parsePaneListJSON(out)
}

func (r *ExecRunner) Kill(ctx context.Context, ref PaneRef) error {
	_, err := r.run(ctx, "", "kill", ref.Name)
	return err
}

func (r *ExecRunner) Remove(ctx context.Context, ref PaneRef) error {
	_, err := r.run(ctx, "", "rm", ref.Name)
	return err
}

func parsePaneListJSON(data []byte) ([]PaneInfo, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var direct []PaneInfo
	if err := json.Unmarshal(trimmed, &direct); err == nil {
		return direct, nil
	}
	var envelope struct {
		Terminals []PaneInfo `json:"terminals"`
		Panes     []PaneInfo `json:"panes"`
		Items     []PaneInfo `json:"items"`
		Sessions  []PaneInfo `json:"sessions"`
	}
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return nil, fmt.Errorf("decode hc ls --json: %w", err)
	}
	switch {
	case envelope.Terminals != nil:
		return envelope.Terminals, nil
	case envelope.Panes != nil:
		return envelope.Panes, nil
	case envelope.Items != nil:
		return envelope.Items, nil
	default:
		return envelope.Sessions, nil
	}
}

var (
	ansiOSCRe = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	ansiCSIRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiESCRe = regexp.MustCompile(`\x1b[@-Z\\-_]`)
)

// StripANSI removes terminal escape sequences and normalizes line endings for clean LLM ingestion.
func StripANSI(raw string) string {
	cleaned := ansiOSCRe.ReplaceAllString(raw, "")
	cleaned = ansiCSIRe.ReplaceAllString(cleaned, "")
	cleaned = ansiESCRe.ReplaceAllString(cleaned, "")
	cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
	if strings.ContainsRune(cleaned, '\r') {
		lines := strings.Split(cleaned, "\n")
		for i, line := range lines {
			if idx := strings.LastIndexByte(line, '\r'); idx >= 0 {
				lines[i] = line[idx+1:]
			}
		}
		cleaned = strings.Join(lines, "\n")
	}
	return strings.TrimRight(cleaned, "\n")
}

// OutputDelta extracts newly appended output when curr extends prev, or falls back to curr.
func OutputDelta(prev, curr string) string {
	prev = strings.TrimRight(prev, "\n")
	curr = strings.TrimRight(curr, "\n")
	if prev == "" {
		return curr
	}
	if curr == prev {
		return ""
	}
	if strings.HasPrefix(curr, prev) {
		return strings.TrimLeft(curr[len(prev):], "\n")
	}
	prevLines := strings.Split(prev, "\n")
	currLines := strings.Split(curr, "\n")
	maxOverlap := min(len(prevLines), len(currLines))
	for overlap := maxOverlap; overlap > 0; overlap-- {
		match := true
		for i := 0; i < overlap; i++ {
			if prevLines[len(prevLines)-overlap+i] != currLines[i] {
				match = false
				break
			}
		}
		if match {
			return strings.Join(currLines[overlap:], "\n")
		}
	}
	return curr
}
