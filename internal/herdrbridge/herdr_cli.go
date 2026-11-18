package herdrbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
)

const cageWorkspaceLabel = "cage-agents"

// HerdrRunner invokes the `herdr` CLI (`herdr` 0.9+) over its local API socket.
type HerdrRunner struct {
	HerdrPath  string
	SocketPath string
	Session    string
}

func NewHerdrRunner(herdrPath, socketPath, session string) *HerdrRunner {
	return &HerdrRunner{
		HerdrPath:  strings.TrimSpace(herdrPath),
		SocketPath: strings.TrimSpace(socketPath),
		Session:    strings.TrimSpace(session),
	}
}

func resolveHerdrBinary(configured string) (string, error) {
	return resolveNamedBinary(configured, "herdr")
}

func (r *HerdrRunner) commandEnv() []string {
	env := os.Environ()
	if r.SocketPath != "" {
		env = setEnv(env, "HERDR_SOCKET_PATH", r.SocketPath)
	}
	return env
}

func (r *HerdrRunner) withGlobalArgs(args ...string) []string {
	if r.Session == "" {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "--session", r.Session)
	return append(out, args...)
}

type herdrErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func parseHerdrError(stderr, stdout []byte) (code, msg string, ok bool) {
	for _, raw := range [][]byte{bytes.TrimSpace(stderr), bytes.TrimSpace(stdout)} {
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var env herdrErrorEnvelope
		if json.Unmarshal(raw, &env) == nil && env.Error.Code != "" {
			return env.Error.Code, env.Error.Message, true
		}
	}
	return "", "", false
}

func (r *HerdrRunner) run(ctx context.Context, args ...string) ([]byte, string, error) {
	bin, err := resolveHerdrBinary(r.HerdrPath)
	if err != nil {
		return nil, "", err
	}
	fullArgs := r.withGlobalArgs(args...)
	cmd := exec.CommandContext(ctx, bin, fullArgs...)
	cmd.Env = r.commandEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if code, msg, ok := parseHerdrError(stderr.Bytes(), stdout.Bytes()); ok {
			return stdout.Bytes(), code, fmt.Errorf("herdr %s: [%s] %s", strings.Join(args, " "), code, msg)
		}
		text := strings.TrimSpace(stderr.String())
		if text == "" {
			text = strings.TrimSpace(stdout.String())
		}
		if text != "" {
			return stdout.Bytes(), "", fmt.Errorf("herdr %s: %w: %s", strings.Join(args, " "), err, text)
		}
		return stdout.Bytes(), "", fmt.Errorf("herdr %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), "", nil
}

type herdrStatusSummary struct {
	ClientVersion string
	ServerStatus  string
	ServerVersion string
	SocketPath    string
}

func parseHerdrStatus(raw string) herdrStatusSummary {
	var out herdrStatusSummary
	section := ""
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(trimmed, ":") {
			section = strings.TrimSuffix(trimmed, ":")
			continue
		}
		key, val, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch section + "." + key {
		case "client.version":
			out.ClientVersion = val
		case "server.status":
			out.ServerStatus = val
		case "server.version":
			out.ServerVersion = val
		case "server.socket":
			out.SocketPath = val
		}
	}
	return out
}

func (r *HerdrRunner) Health(ctx context.Context) HealthInfo {
	bin, err := resolveHerdrBinary(r.HerdrPath)
	if err != nil {
		return HealthInfo{Available: false, Backend: "herdr", Error: err.Error()}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _, err := r.run(probeCtx, "status")
	status := parseHerdrStatus(string(out))
	version := status.ServerVersion
	if version == "" {
		version = status.ClientVersion
	}
	if err != nil {
		return HealthInfo{
			Available:  false,
			Backend:    "herdr",
			BinaryPath: bin,
			Version:    version,
			SocketPath: status.SocketPath,
			Error:      err.Error(),
		}
	}
	if !strings.EqualFold(status.ServerStatus, "running") {
		msg := "herdr server is not running"
		if status.ServerStatus != "" {
			msg = "herdr server status: " + status.ServerStatus
		}
		return HealthInfo{
			Available:  false,
			Backend:    "herdr",
			BinaryPath: bin,
			Version:    version,
			SocketPath: status.SocketPath,
			Error:      msg,
		}
	}
	return HealthInfo{
		Available:  true,
		Backend:    "herdr",
		BinaryPath: bin,
		Version:    version,
		SocketPath: status.SocketPath,
	}
}

func (r *HerdrRunner) ensurePane(ctx context.Context, label, workdir string) (string, error) {
	wsOut, _, err := r.run(ctx, "workspace", "list")
	if err == nil {
		var wsList struct {
			Result struct {
				Workspaces []struct {
					WorkspaceID string `json:"workspace_id"`
					Label       string `json:"label"`
				} `json:"workspaces"`
			} `json:"result"`
		}
		if json.Unmarshal(wsOut, &wsList) == nil {
			for _, ws := range wsList.Result.Workspaces {
				if ws.Label == cageWorkspaceLabel && ws.WorkspaceID != "" {
					tabOut, _, tabErr := r.run(ctx, "tab", "create", "--workspace", ws.WorkspaceID, "--cwd", workdir, "--label", label, "--no-focus")
					if tabErr == nil {
						var tabCreated struct {
							Result struct {
								RootPane struct {
									PaneID string `json:"pane_id"`
								} `json:"root_pane"`
							} `json:"result"`
						}
						if json.Unmarshal(tabOut, &tabCreated) == nil && tabCreated.Result.RootPane.PaneID != "" {
							return tabCreated.Result.RootPane.PaneID, nil
						}
					}
					break
				}
			}
		}
	}

	wsCreateOut, _, err := r.run(ctx, "workspace", "create", "--cwd", workdir, "--label", cageWorkspaceLabel, "--no-focus")
	if err != nil {
		return "", err
	}
	var wsCreated struct {
		Result struct {
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(wsCreateOut, &wsCreated); err != nil || wsCreated.Result.RootPane.PaneID == "" {
		return "", fmt.Errorf("herdr workspace create did not return root_pane.pane_id")
	}
	return wsCreated.Result.RootPane.PaneID, nil
}

func (r *HerdrRunner) RunBackground(ctx context.Context, opts RunOptions) (string, bool, error) {
	paneID, err := r.ensurePane(ctx, opts.PaneName, opts.Workdir)
	if err != nil {
		return "", false, err
	}
	if opts.Interactive && opts.HerdrKind != "" {
		args := []string{"agent", "start", opts.PaneName, "--kind", opts.HerdrKind, "--pane", paneID}
		if len(opts.Args) > 0 {
			args = append(args, "--")
			args = append(args, opts.Args...)
		}
		_, code, err := r.run(ctx, args...)
		if err != nil && code != "agent_not_ready" {
			_, _, _ = r.run(ctx, "pane", "close", paneID)
			return "", true, err
		}
		return paneID, true, nil
	}

	cmdArgs := append([]string{opts.Command}, opts.Args...)
	cmdLine := shellescape.QuoteCommand(cmdArgs)
	if _, _, err := r.run(ctx, "pane", "run", paneID, cmdLine); err != nil {
		_, _, _ = r.run(ctx, "pane", "close", paneID)
		return "", false, err
	}
	return paneID, false, nil
}

func (r *HerdrRunner) Send(ctx context.Context, ref PaneRef, text string) error {
	if ref.IsAgent {
		_, code, err := r.run(ctx, "agent", "prompt", ref.Name, text)
		if err != nil && code == "agent_blocked" {
			return nil
		}
		return err
	}
	if ref.PaneID == "" {
		return errors.New("pane_id is required to send text")
	}
	_, _, err := r.run(ctx, "pane", "send-text", ref.PaneID, text+"\n")
	return err
}

func (r *HerdrRunner) PromptAndWait(ctx context.Context, ref PaneRef, text string, timeout time.Duration) (WaitResult, error) {
	if !ref.IsAgent {
		if err := r.Send(ctx, ref, text); err != nil {
			return WaitResult{}, err
		}
		return r.Wait(ctx, ref, timeout)
	}
	ms := max(1, int(timeout/time.Millisecond))
	out, code, err := r.run(ctx, "agent", "prompt", ref.Name, text, "--wait", "--timeout", strconv.Itoa(ms))
	if err != nil {
		switch code {
		case "agent_blocked":
			return WaitResult{Blocked: true}, nil
		case "timeout", "agent_prompt_stalled":
			return WaitResult{TimedOut: true}, nil
		default:
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return WaitResult{TimedOut: true}, nil
			}
			return WaitResult{}, err
		}
	}
	return parseHerdrAgentWaitOutput(out), nil
}

func (r *HerdrRunner) Wait(ctx context.Context, ref PaneRef, timeout time.Duration) (WaitResult, error) {
	if !ref.IsAgent {
		// Non-agent pane commands (such as `codex exec`) settle briefly without agent state tracking.
		wait := min(timeout, 2*time.Second)
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return WaitResult{}, ctx.Err()
		case <-timer.C:
			return WaitResult{Completed: true}, nil
		}
	}
	ms := max(1, int(timeout/time.Millisecond))
	out, code, err := r.run(ctx, "agent", "wait", ref.Name, "--timeout", strconv.Itoa(ms))
	if err != nil {
		if code == "timeout" || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return WaitResult{TimedOut: true}, nil
		}
		return WaitResult{}, err
	}
	return parseHerdrAgentWaitOutput(out), nil
}

func parseHerdrAgentWaitOutput(out []byte) WaitResult {
	var resp struct {
		Result struct {
			Agent struct {
				AgentStatus string `json:"agent_status"`
			} `json:"agent"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &resp) == nil {
		if resp.Result.Agent.AgentStatus == "blocked" {
			return WaitResult{Blocked: true}
		}
	}
	return WaitResult{Completed: true}
}

func (r *HerdrRunner) Read(ctx context.Context, ref PaneRef, lines int) (string, error) {
	if lines <= 0 {
		lines = DefaultReadLines
	}
	if ref.IsAgent && ref.Name != "" {
		out, _, err := r.run(ctx, "agent", "read", ref.Name, "--source", "recent-unwrapped", "--lines", strconv.Itoa(lines))
		if err == nil {
			return parseHerdrReadText(out), nil
		}
	}
	if ref.PaneID != "" {
		out, _, err := r.run(ctx, "pane", "read", ref.PaneID, "--source", "recent-unwrapped", "--lines", strconv.Itoa(lines))
		if err != nil {
			return "", err
		}
		return parseHerdrReadText(out), nil
	}
	return "", errors.New("pane target not found")
}

func parseHerdrReadText(raw []byte) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var resp struct {
			Result struct {
				Text string `json:"text"`
				Read struct {
					Text string `json:"text"`
				} `json:"read"`
			} `json:"result"`
		}
		if json.Unmarshal(trimmed, &resp) == nil {
			if resp.Result.Read.Text != "" {
				return StripANSI(resp.Result.Read.Text)
			}
			if resp.Result.Text != "" {
				return StripANSI(resp.Result.Text)
			}
			return ""
		}
	}
	return StripANSI(string(raw))
}

func (r *HerdrRunner) List(ctx context.Context) ([]PaneInfo, error) {
	paneOut, _, err := r.run(ctx, "pane", "list")
	if err != nil {
		return nil, err
	}
	var paneResp struct {
		Result struct {
			Panes []PaneInfo `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(paneOut, &paneResp); err != nil {
		return nil, fmt.Errorf("decode herdr pane list: %w", err)
	}
	byPane := make(map[string]PaneInfo, len(paneResp.Result.Panes))
	order := make([]string, 0, len(paneResp.Result.Panes))
	for _, p := range paneResp.Result.Panes {
		p.ID = p.PaneID
		p.Status = p.AgentStatus
		byPane[p.PaneID] = p
		order = append(order, p.PaneID)
	}

	if agentOut, _, agentErr := r.run(ctx, "agent", "list"); agentErr == nil {
		var agentResp struct {
			Result struct {
				Agents []PaneInfo `json:"agents"`
			} `json:"result"`
		}
		if json.Unmarshal(agentOut, &agentResp) == nil {
			for _, a := range agentResp.Result.Agents {
				if p, ok := byPane[a.PaneID]; ok {
					if a.Name != "" {
						p.Name = a.Name
					}
					if a.AgentStatus != "" {
						p.AgentStatus = a.AgentStatus
						p.Status = a.AgentStatus
					}
					byPane[a.PaneID] = p
				} else {
					a.ID = a.PaneID
					a.Status = a.AgentStatus
					byPane[a.PaneID] = a
					order = append(order, a.PaneID)
				}
			}
		}
	}

	out := make([]PaneInfo, 0, len(order))
	for _, id := range order {
		out = append(out, byPane[id])
	}
	return out, nil
}

func (r *HerdrRunner) Kill(ctx context.Context, ref PaneRef) error {
	if ref.PaneID != "" {
		_, _, err := r.run(ctx, "pane", "close", ref.PaneID)
		return err
	}
	panes, err := r.List(ctx)
	if err != nil {
		return err
	}
	for _, p := range panes {
		if p.Name == ref.Name && p.PaneID != "" {
			_, _, err := r.run(ctx, "pane", "close", p.PaneID)
			return err
		}
	}
	return nil
}

func (r *HerdrRunner) Remove(context.Context, PaneRef) error {
	// `herdr pane close` already removes the pane from layout.
	return nil
}
