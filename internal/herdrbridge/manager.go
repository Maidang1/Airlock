package herdrbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	SessionStatusIdle            = "idle"
	SessionStatusWorking         = "working"
	SessionStatusWaitingApproval = "waiting_approval"
	SessionStatusExited          = "exited"
	SessionStatusClosed          = "closed"
)

// PathResolver maps a requested guest or host directory to an authorized host directory.
type PathResolver func(requestedWorkdir string) (hostDir string, err error)

// ModeView describes an allowed mode for a host agent profile.
type ModeView struct {
	Name        string `json:"name"`
	Interactive bool   `json:"interactive"`
	ReadOnly    bool   `json:"read_only"`
}

// ProfileView describes an available host agent profile without exposing host binary internals.
type ProfileView struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	DefaultMode string     `json:"default_mode"`
	Modes       []ModeView `json:"modes"`
}

// SessionView is the safe, structured session snapshot returned to callers and UI.
type SessionView struct {
	SessionID   string    `json:"session_id"`
	PaneName    string    `json:"pane_name"`
	PaneID      string    `json:"pane_id,omitempty"`
	Agent       string    `json:"agent"`
	Mode        string    `json:"mode"`
	Interactive bool      `json:"interactive"`
	ReadOnly    bool      `json:"read_only"`
	Workdir     string    `json:"workdir"`
	Status      string    `json:"status"`
	HCStatus    string    `json:"hc_status,omitempty"`
	Output      string    `json:"output,omitempty"`
	NewOutput   string    `json:"new_output,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Closed      bool      `json:"closed,omitempty"`
}

// ListAgentsResult summarizes Herdr connectivity, whitelisted profiles, and active sessions.
type ListAgentsResult struct {
	HerdrAvailable bool          `json:"herdr_available"`
	HerdrError     string        `json:"herdr_error,omitempty"`
	Backend        string        `json:"backend,omitempty"`
	BinaryPath     string        `json:"binary_path,omitempty"`
	HCPath         string        `json:"hc_path,omitempty"`
	Version        string        `json:"version,omitempty"`
	SocketPath     string        `json:"socket_path,omitempty"`
	ConfigPath     string        `json:"config_path,omitempty"`
	Profiles       []ProfileView `json:"profiles"`
	ActiveSessions []SessionView `json:"active_sessions"`
}

// StartRequest configures starting a host agent session via Herdr.
type StartRequest struct {
	Agent          string `json:"agent"`
	Mode           string `json:"mode,omitempty"`
	Workdir        string `json:"workdir"`
	InitialPrompt  string `json:"initial_prompt,omitempty"`
	WaitTimeoutSec int    `json:"wait_timeout_seconds,omitempty"`
}

// SendRequest sends an interactive follow-up message to an active session.
type SendRequest struct {
	SessionID      string `json:"session_id"`
	Message        string `json:"message"`
	WaitTimeoutSec int    `json:"wait_timeout_seconds,omitempty"`
}

// ReadRequest inspects an existing session's current state and screen output.
type ReadRequest struct {
	SessionID      string `json:"session_id"`
	Lines          int    `json:"lines,omitempty"`
	WaitTimeoutSec int    `json:"wait_timeout_seconds,omitempty"`
}

type managedSession struct {
	mu         sync.Mutex
	id         string
	paneName   string
	paneID     string
	isAgent    bool
	agent      string
	modeName   string
	mode       AgentMode
	workdir    string
	status     string
	hcStatus   string
	lastOutput string
	createdAt  time.Time
	updatedAt  time.Time
	closed     bool
}

func (s *managedSession) refLocked() PaneRef {
	return PaneRef{
		Name:    s.paneName,
		PaneID:  s.paneID,
		IsAgent: s.isAgent,
	}
}

func (s *managedSession) viewLocked(output, newOutput string) SessionView {
	return SessionView{
		SessionID:   s.id,
		PaneName:    s.paneName,
		PaneID:      s.paneID,
		Agent:       s.agent,
		Mode:        s.modeName,
		Interactive: s.mode.Interactive,
		ReadOnly:    s.mode.ReadOnly,
		Workdir:     s.workdir,
		Status:      s.status,
		HCStatus:    s.hcStatus,
		Output:      output,
		NewOutput:   newOutput,
		CreatedAt:   s.createdAt,
		UpdatedAt:   s.updatedAt,
		Closed:      s.closed,
	}
}

// Manager coordinates whitelisted host agent sessions on a single Herdr core.
type Manager struct {
	cfg           Config
	runner        Runner
	resolvePath   PathResolver
	startupSettle time.Duration

	mu       sync.Mutex
	sessions map[string]*managedSession
}

// NewManager creates a Manager with the provided config, runner, and workdir resolver.
func NewManager(cfg Config, runner Runner, resolvePath PathResolver) *Manager {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = DefaultMaxSessions
	}
	if cfg.DefaultWaitTimeout <= 0 {
		cfg.DefaultWaitTimeout = DefaultWaitTimeout
	}
	return &Manager{
		cfg:           cfg,
		runner:        runner,
		resolvePath:   resolvePath,
		startupSettle: 350 * time.Millisecond,
		sessions:      make(map[string]*managedSession),
	}
}

// ListAgents returns Herdr health, whitelisted agent profiles, and active managed sessions.
func (m *Manager) ListAgents(ctx context.Context) ListAgentsResult {
	health := m.runner.Health(ctx)
	profiles := make([]ProfileView, 0, len(m.cfg.ProfileOrder))
	for _, name := range m.cfg.ProfileOrder {
		p := m.cfg.Profiles[name]
		defaultMode := p.DefaultMode
		modes := make([]ModeView, 0, len(p.Modes))
		modeNames := make([]string, 0, len(p.Modes))
		for k := range p.Modes {
			modeNames = append(modeNames, k)
		}
		slices.Sort(modeNames)
		if defaultMode == "" && len(modeNames) > 0 {
			defaultMode = modeNames[0]
		}
		for _, modeName := range modeNames {
			mode := p.Modes[modeName]
			modes = append(modes, ModeView{
				Name:        modeName,
				Interactive: mode.Interactive,
				ReadOnly:    mode.ReadOnly,
			})
		}
		profiles = append(profiles, ProfileView{
			Name:        p.Name,
			Description: p.Description,
			DefaultMode: defaultMode,
			Modes:       modes,
		})
	}

	var panes []PaneInfo
	var listLoaded bool
	if health.Available {
		if listed, err := m.runner.List(ctx); err == nil {
			panes = listed
			listLoaded = true
		}
	}

	m.mu.Lock()
	sessions := make([]*managedSession, 0, len(m.sessions))
	for _, sess := range m.sessions {
		sessions = append(sessions, sess)
	}
	m.mu.Unlock()

	views := make([]SessionView, 0, len(sessions))
	for _, sess := range sessions {
		sess.mu.Lock()
		if sess.closed {
			sess.mu.Unlock()
			continue
		}
		if listLoaded {
			pane, ok := matchPane(panes, sess.refLocked())
			sess.status, sess.hcStatus = classifyPaneStatus(pane, ok, WaitResult{}, sess.status)
		}
		views = append(views, sess.viewLocked("", ""))
		sess.mu.Unlock()
	}
	slices.SortFunc(views, func(a, b SessionView) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})

	hcPath := health.HCPath
	if hcPath == "" {
		hcPath = health.BinaryPath
	}
	return ListAgentsResult{
		HerdrAvailable: health.Available,
		HerdrError:     health.Error,
		Backend:        health.Backend,
		BinaryPath:     health.BinaryPath,
		HCPath:         hcPath,
		Version:        health.Version,
		SocketPath:     health.SocketPath,
		ConfigPath:     m.cfg.ConfigPath,
		Profiles:       profiles,
		ActiveSessions: views,
	}
}

// StartSession launches a whitelisted host agent inside a new Herdr pane.
func (m *Manager) StartSession(ctx context.Context, req StartRequest) (SessionView, error) {
	if m.resolvePath == nil {
		return SessionView{}, errors.New("workdir resolver is not configured")
	}
	agentName := strings.TrimSpace(req.Agent)
	profile, ok := m.cfg.Profiles[agentName]
	if !ok {
		return SessionView{}, fmt.Errorf("agent %q is not whitelisted", agentName)
	}
	modeName, mode, err := profile.resolveMode(req.Mode)
	if err != nil {
		return SessionView{}, err
	}
	if strings.ContainsRune(req.InitialPrompt, '\x00') {
		return SessionView{}, errors.New("initial_prompt contains invalid NUL byte")
	}
	if !mode.Interactive && strings.TrimSpace(req.InitialPrompt) == "" {
		return SessionView{}, fmt.Errorf("mode %q for agent %q requires initial_prompt", modeName, agentName)
	}
	hostDir, err := m.resolvePath(req.Workdir)
	if err != nil {
		return SessionView{}, err
	}
	if health := m.runner.Health(ctx); !health.Available {
		return SessionView{}, fmt.Errorf("herdr is unavailable: %s", health.Error)
	}

	sessionID := shortID()
	paneName := PaneNamePrefix + strings.ToLower(profile.Name) + "-" + sessionID
	now := time.Now()
	sess := &managedSession{
		id:        sessionID,
		paneName:  paneName,
		agent:     profile.Name,
		modeName:  modeName,
		mode:      mode,
		workdir:   hostDir,
		status:    SessionStatusWorking,
		createdAt: now,
		updatedAt: now,
	}

	m.mu.Lock()
	activeCount := 0
	for _, existing := range m.sessions {
		existing.mu.Lock()
		closed := existing.closed
		existingStatus := existing.status
		existingReadOnly := existing.mode.ReadOnly
		existingDir := existing.workdir
		existingID := existing.id
		existing.mu.Unlock()
		if closed || existingStatus == SessionStatusExited {
			continue
		}
		activeCount++
		if !mode.ReadOnly && !existingReadOnly && workdirsOverlap(hostDir, existingDir) {
			m.mu.Unlock()
			return SessionView{}, fmt.Errorf("active write session %q already uses %s; reuse it, close it, or use a separate worktree", existingID, existingDir)
		}
	}
	if activeCount >= m.cfg.MaxSessions {
		m.mu.Unlock()
		return SessionView{}, fmt.Errorf("active Herdr bridge session limit reached (%d)", m.cfg.MaxSessions)
	}
	m.sessions[sessionID] = sess
	m.mu.Unlock()

	args := slices.Clone(mode.Args)
	if profile.WorkdirFlag != "" {
		args = append(args, profile.WorkdirFlag, hostDir)
	}
	if !mode.Interactive {
		args = append(args, req.InitialPrompt)
	}

	paneID, isAgent, err := m.runner.RunBackground(ctx, RunOptions{
		PaneName:    paneName,
		Workdir:     hostDir,
		Command:     profile.Command,
		HerdrKind:   profile.HerdrKind,
		Interactive: mode.Interactive,
		Args:        args,
	})
	if err != nil {
		m.mu.Lock()
		delete(m.sessions, sessionID)
		m.mu.Unlock()
		return SessionView{}, err
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.paneID = paneID
	sess.isAgent = isAgent
	ref := sess.refLocked()

	if mode.Interactive && strings.TrimSpace(req.InitialPrompt) != "" {
		if !isAgent && m.startupSettle > 0 {
			timer := time.NewTimer(m.startupSettle)
			select {
			case <-ctx.Done():
				timer.Stop()
				return sess.viewLocked("", ""), ctx.Err()
			case <-timer.C:
			}
		}
		waitTimeout := m.clampWaitTimeout(req.WaitTimeoutSec, m.cfg.DefaultWaitTimeout)
		waitRes, promptErr := m.runner.PromptAndWait(ctx, ref, req.InitialPrompt, waitTimeout)
		return m.finishWaitSnapshotLocked(ctx, sess, waitRes, promptErr, DefaultReadLines, "")
	}

	if strings.TrimSpace(req.InitialPrompt) != "" || req.WaitTimeoutSec > 0 {
		waitTimeout := m.clampWaitTimeout(req.WaitTimeoutSec, 30*time.Second)
		return m.waitAndSnapshotLocked(ctx, sess, waitTimeout, DefaultReadLines, "")
	}

	output, _ := m.runner.Read(ctx, ref, DefaultReadLines)
	pane, found := m.findPane(ctx, ref)
	sess.status, sess.hcStatus = classifyPaneStatus(pane, found, WaitResult{}, SessionStatusIdle)
	sess.lastOutput = output
	sess.updatedAt = time.Now()
	return sess.viewLocked(output, output), nil
}

// SendMessage steers an active interactive session and waits up to the timeout for output.
func (m *Manager) SendMessage(ctx context.Context, req SendRequest) (SessionView, error) {
	if strings.TrimSpace(req.Message) == "" {
		return SessionView{}, errors.New("message must not be empty")
	}
	if strings.ContainsRune(req.Message, '\x00') {
		return SessionView{}, errors.New("message contains invalid NUL byte")
	}
	sess, err := m.getSession(req.SessionID)
	if err != nil {
		return SessionView{}, err
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.closed {
		return SessionView{}, fmt.Errorf("session %q is closed", req.SessionID)
	}
	if !sess.mode.Interactive {
		return SessionView{}, fmt.Errorf("session %q uses non-interactive mode %q", req.SessionID, sess.modeName)
	}

	ref := sess.refLocked()
	if pane, found := m.findPane(ctx, ref); !found || isPaneExited(pane) {
		sess.status, sess.hcStatus = SessionStatusExited, paneStatusText(pane)
		sess.updatedAt = time.Now()
		return SessionView{}, fmt.Errorf("session %q has already exited", req.SessionID)
	}

	prevOutput, err := m.runner.Read(ctx, ref, MaxReadLines)
	if err != nil {
		prevOutput = sess.lastOutput
	}
	waitTimeout := m.clampWaitTimeout(req.WaitTimeoutSec, m.cfg.DefaultWaitTimeout)
	waitRes, promptErr := m.runner.PromptAndWait(ctx, ref, req.Message, waitTimeout)
	return m.finishWaitSnapshotLocked(ctx, sess, waitRes, promptErr, DefaultReadLines, prevOutput)
}

// ReadSession inspects an existing session's status and screen output.
func (m *Manager) ReadSession(ctx context.Context, req ReadRequest) (SessionView, error) {
	sess, err := m.getSession(req.SessionID)
	if err != nil {
		return SessionView{}, err
	}
	lines := req.Lines
	if lines <= 0 {
		lines = DefaultReadLines
	}
	if lines > MaxReadLines {
		lines = MaxReadLines
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.closed {
		return sess.viewLocked(sess.lastOutput, ""), nil
	}
	prev := sess.lastOutput
	if req.WaitTimeoutSec > 0 {
		waitTimeout := m.clampWaitTimeout(req.WaitTimeoutSec, m.cfg.DefaultWaitTimeout)
		return m.waitAndSnapshotLocked(ctx, sess, waitTimeout, lines, prev)
	}

	ref := sess.refLocked()
	output, readErr := m.runner.Read(ctx, ref, lines)
	pane, found := m.findPane(ctx, ref)
	sess.status, sess.hcStatus = classifyPaneStatus(pane, found, WaitResult{}, sess.status)
	if readErr == nil {
		delta := OutputDelta(prev, output)
		sess.lastOutput = output
		sess.updatedAt = time.Now()
		return sess.viewLocked(output, delta), nil
	}
	if sess.status == SessionStatusExited {
		sess.updatedAt = time.Now()
		return sess.viewLocked(sess.lastOutput, ""), nil
	}
	return SessionView{}, readErr
}

// CloseSession terminates a bridge-managed pane and marks the session closed.
func (m *Manager) CloseSession(ctx context.Context, sessionID string) (SessionView, error) {
	sess, err := m.getSession(sessionID)
	if err != nil {
		return SessionView{}, err
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.closed {
		return sess.viewLocked(sess.lastOutput, ""), nil
	}
	ref := sess.refLocked()
	if pane, found := m.findPane(ctx, ref); found {
		if !isPaneExited(pane) {
			if err := m.runner.Kill(ctx, ref); err != nil {
				if p2, stillFound := m.findPane(ctx, ref); stillFound && !isPaneExited(p2) {
					return SessionView{}, err
				}
			}
		}
		_ = m.runner.Remove(ctx, ref)
	}
	sess.closed = true
	sess.status = SessionStatusClosed
	sess.updatedAt = time.Now()
	return sess.viewLocked(sess.lastOutput, ""), nil
}

// CloseAll terminates all open bridge-managed sessions during shutdown.
func (m *Manager) CloseAll(ctx context.Context) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_, _ = m.CloseSession(ctx, id)
	}
}

func (m *Manager) waitAndSnapshotLocked(ctx context.Context, sess *managedSession, timeout time.Duration, lines int, prevOutput string) (SessionView, error) {
	waitRes, waitErr := m.runner.Wait(ctx, sess.refLocked(), timeout)
	return m.finishWaitSnapshotLocked(ctx, sess, waitRes, waitErr, lines, prevOutput)
}

func (m *Manager) finishWaitSnapshotLocked(ctx context.Context, sess *managedSession, waitRes WaitResult, waitErr error, lines int, prevOutput string) (SessionView, error) {
	ref := sess.refLocked()
	output, readErr := m.runner.Read(ctx, ref, lines)
	if readErr != nil {
		output = sess.lastOutput
		if waitRes.Output != "" {
			output = waitRes.Output
		}
	}
	pane, found := m.findPane(ctx, ref)
	sess.status, sess.hcStatus = classifyPaneStatus(pane, found, waitRes, sess.status)
	if waitErr != nil && !waitRes.TimedOut && !waitRes.Blocked && sess.status != SessionStatusExited && readErr != nil {
		return SessionView{}, waitErr
	}
	delta := OutputDelta(prevOutput, output)
	sess.lastOutput = output
	sess.updatedAt = time.Now()
	return sess.viewLocked(output, delta), nil
}

func (m *Manager) getSession(id string) (*managedSession, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("session_id is required")
	}
	m.mu.Lock()
	sess, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("session %q was not created by this bridge", id)
	}
	return sess, nil
}

func (m *Manager) findPane(ctx context.Context, ref PaneRef) (PaneInfo, bool) {
	panes, err := m.runner.List(ctx)
	if err != nil {
		return PaneInfo{}, false
	}
	return matchPane(panes, ref)
}

func matchPane(panes []PaneInfo, ref PaneRef) (PaneInfo, bool) {
	for _, pane := range panes {
		if ref.Name != "" && pane.Name == ref.Name {
			return pane, true
		}
		if ref.PaneID != "" && (pane.PaneID == ref.PaneID || pane.ID == ref.PaneID) {
			return pane, true
		}
	}
	return PaneInfo{}, false
}

func (m *Manager) clampWaitTimeout(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	d := time.Duration(seconds) * time.Second
	if d > MaxWaitTimeout {
		return MaxWaitTimeout
	}
	return d
}

func classifyPaneStatus(pane PaneInfo, found bool, waitRes WaitResult, previous string) (string, string) {
	if !found {
		return SessionStatusExited, "missing"
	}
	raw := paneStatusText(pane)
	lower := strings.ToLower(raw)
	switch {
	case isPaneExited(pane):
		return SessionStatusExited, raw
	case waitRes.Blocked || (pane.Blocked != nil && *pane.Blocked) || (pane.WaitingApproval != nil && *pane.WaitingApproval) ||
		strings.Contains(lower, "block") || strings.Contains(lower, "approval") || strings.Contains(lower, "wait_input") || strings.Contains(lower, "permission"):
		return SessionStatusWaitingApproval, raw
	case waitRes.TimedOut:
		return SessionStatusWorking, raw
	case strings.Contains(lower, "idle") || strings.Contains(lower, "ready") || strings.EqualFold(pane.AgentStatus, "done"):
		return SessionStatusIdle, raw
	case strings.Contains(lower, "work") || strings.Contains(lower, "run") || strings.Contains(lower, "busy"):
		if !waitRes.TimedOut && previous != "" {
			return SessionStatusWorking, raw
		}
		return SessionStatusWorking, raw
	default:
		return SessionStatusIdle, raw
	}
}

func paneStatusText(pane PaneInfo) string {
	if pane.AgentStatus != "" {
		return pane.AgentStatus
	}
	if pane.Status != "" {
		return pane.Status
	}
	if pane.State != "" {
		return pane.State
	}
	if pane.Exited != nil && *pane.Exited {
		return "exited"
	}
	if pane.Blocked != nil && *pane.Blocked {
		return "blocked"
	}
	if pane.WaitingApproval != nil && *pane.WaitingApproval {
		return "waiting_approval"
	}
	if pane.Running != nil {
		if *pane.Running {
			return "running"
		}
		return "exited"
	}
	return ""
}

func isPaneExited(pane PaneInfo) bool {
	if pane.Exited != nil && *pane.Exited {
		return true
	}
	if pane.Running != nil && !*pane.Running {
		return true
	}
	if pane.ExitCode != nil {
		return true
	}
	// In herdr 0.9+, agent_status="done" means the agent finished a turn and is ready for input.
	if strings.EqualFold(pane.AgentStatus, "done") {
		return false
	}
	lower := strings.ToLower(paneStatusText(pane))
	return strings.Contains(lower, "exit") || strings.Contains(lower, "done") || strings.Contains(lower, "stop") || strings.Contains(lower, "dead") || strings.Contains(lower, "kill")
}

func workdirsOverlap(a, b string) bool {
	ca := filepath.Clean(a)
	cb := filepath.Clean(b)
	return pathContains(ca, cb) || pathContains(cb, ca)
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func shortID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
