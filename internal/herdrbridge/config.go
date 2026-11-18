// Package herdrbridge exposes a restricted host-side MCP bridge over a local
// herdr-core (hc) instance so sandboxed guest agents can dispatch and steer
// whitelisted host CLI agents interactively without direct socket access.
package herdrbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	DefaultMaxSessions        = 6
	DefaultWaitTimeout        = 90 * time.Second
	MaxWaitTimeout            = 600 * time.Second
	DefaultReadLines          = 120
	MaxReadLines              = 500
	DefaultConfigFileName     = "herdr-agents.json"
	PaneNamePrefix            = "cage-"
)

var validProfileName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`)
var validModeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`)

// AgentMode defines a fixed, pre-approved flag preset for an external agent.
type AgentMode struct {
	Args        []string `json:"args"`
	Interactive bool     `json:"interactive"`
	ReadOnly    bool     `json:"read_only"`
}

// AgentProfile defines a whitelisted host CLI agent reachable via Herdr.
type AgentProfile struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Command     string               `json:"command"`
	HerdrKind   string               `json:"herdr_kind,omitempty"`
	WorkdirFlag string               `json:"workdir_flag,omitempty"`
	DefaultMode string               `json:"default_mode,omitempty"`
	Modes       map[string]AgentMode `json:"modes"`
	Disabled    bool                 `json:"disabled,omitempty"`
}

// FileConfig is the optional user configuration stored at <profile>/herdr-agents.json.
type FileConfig struct {
	HerdrPath         string         `json:"herdr_path,omitempty"`
	HCPath            string         `json:"hc_path,omitempty"`
	SocketPath        string         `json:"socket_path,omitempty"`
	Session           string         `json:"session,omitempty"`
	MaxSessions       int            `json:"max_sessions,omitempty"`
	DefaultWaitSecond int            `json:"default_wait_seconds,omitempty"`
	Profiles          []AgentProfile `json:"profiles,omitempty"`
}

// Config is the resolved runtime configuration for the Herdr bridge.
type Config struct {
	ConfigPath         string
	HerdrPath          string
	HCPath             string
	SocketPath         string
	Session            string
	MaxSessions        int
	DefaultWaitTimeout time.Duration
	Profiles           map[string]AgentProfile
	ProfileOrder       []string
}

// DefaultProfiles returns the built-in host agent profiles.
func DefaultProfiles() []AgentProfile {
	return []AgentProfile{
		{
			Name:        "codex",
			Description: "OpenAI Codex CLI on the host (supports interactive and exec modes)",
			Command:     "codex",
			HerdrKind:   "codex",
			WorkdirFlag: "-C",
			DefaultMode: "read-only",
			Modes: map[string]AgentMode{
				"read-only": {
					Args:        []string{"--sandbox", "read-only"},
					Interactive: true,
					ReadOnly:    true,
				},
				"workspace-write": {
					Args:        []string{"--sandbox", "workspace-write"},
					Interactive: true,
					ReadOnly:    false,
				},
				"exec-read-only": {
					Args:        []string{"exec", "--sandbox", "read-only"},
					Interactive: false,
					ReadOnly:    true,
				},
				"exec-write": {
					Args:        []string{"exec", "--sandbox", "workspace-write"},
					Interactive: false,
					ReadOnly:    false,
				},
			},
		},
		{
			Name:        "claude",
			Description: "Claude Code CLI on the host",
			Command:     "claude",
			HerdrKind:   "claude",
			DefaultMode: "plan",
			Modes: map[string]AgentMode{
				"plan": {
					Args:        []string{"--permission-mode", "plan"},
					Interactive: true,
					ReadOnly:    true,
				},
				"accept-edits": {
					Args:        []string{"--permission-mode", "acceptEdits"},
					Interactive: true,
					ReadOnly:    false,
				},
			},
		},
		{
			Name:        "opencode",
			Description: "OpenCode CLI on the host",
			Command:     "opencode",
			HerdrKind:   "opencode",
			DefaultMode: "interactive",
			Modes: map[string]AgentMode{
				"interactive": {
					Args:        nil,
					Interactive: true,
					ReadOnly:    false,
				},
			},
		},
	}
}

// LoadConfig loads optional <profileDir>/herdr-agents.json and merges it with
// the built-in agent profiles. Missing config files are not an error.
func LoadConfig(profileDir string) (Config, error) {
	cfg := Config{
		MaxSessions:        DefaultMaxSessions,
		DefaultWaitTimeout: DefaultWaitTimeout,
		Profiles:           make(map[string]AgentProfile),
	}
	if profileDir != "" {
		cfg.ConfigPath = filepath.Join(profileDir, DefaultConfigFileName)
	}
	var fc FileConfig
	if cfg.ConfigPath != "" {
		data, err := os.ReadFile(cfg.ConfigPath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return Config{}, fmt.Errorf("read %s: %w", cfg.ConfigPath, err)
			}
		} else {
			if err := json.Unmarshal(data, &fc); err != nil {
				return Config{}, fmt.Errorf("parse %s: %w", cfg.ConfigPath, err)
			}
		}
	}

	cfg.HerdrPath = strings.TrimSpace(fc.HerdrPath)
	cfg.HCPath = strings.TrimSpace(fc.HCPath)
	cfg.SocketPath = strings.TrimSpace(fc.SocketPath)
	cfg.Session = strings.TrimSpace(fc.Session)
	if fc.MaxSessions > 0 {
		cfg.MaxSessions = fc.MaxSessions
	}
	if fc.DefaultWaitSecond > 0 {
		d := time.Duration(fc.DefaultWaitSecond) * time.Second
		if d > MaxWaitTimeout {
			d = MaxWaitTimeout
		}
		cfg.DefaultWaitTimeout = d
	}

	merged := make(map[string]AgentProfile)
	var order []string
	add := func(p AgentProfile) error {
		if err := validateProfile(p); err != nil {
			return err
		}
		if _, exists := merged[p.Name]; !exists {
			order = append(order, p.Name)
		}
		merged[p.Name] = cloneProfile(p)
		return nil
	}
	for _, p := range DefaultProfiles() {
		if err := add(p); err != nil {
			return Config{}, err
		}
	}
	for _, p := range fc.Profiles {
		if err := add(p); err != nil {
			return Config{}, err
		}
	}
	for _, name := range order {
		p := merged[name]
		if p.Disabled {
			continue
		}
		cfg.Profiles[name] = p
		cfg.ProfileOrder = append(cfg.ProfileOrder, name)
	}
	return cfg, nil
}

func validateProfile(p AgentProfile) error {
	if !validProfileName.MatchString(p.Name) {
		return fmt.Errorf("invalid agent profile name %q", p.Name)
	}
	if p.Disabled {
		return nil
	}
	if strings.TrimSpace(p.Command) == "" || strings.ContainsAny(p.Command, "\x00\r\n") {
		return fmt.Errorf("profile %q has invalid command", p.Name)
	}
	if strings.ContainsAny(p.WorkdirFlag, " \t\x00\r\n") {
		return fmt.Errorf("profile %q has invalid workdir_flag", p.Name)
	}
	if p.HerdrKind != "" && !validProfileName.MatchString(p.HerdrKind) {
		return fmt.Errorf("profile %q has invalid herdr_kind %q", p.Name, p.HerdrKind)
	}
	if len(p.Modes) == 0 {
		return fmt.Errorf("profile %q must define at least one mode", p.Name)
	}
	for modeName, mode := range p.Modes {
		if !validModeName.MatchString(modeName) {
			return fmt.Errorf("profile %q has invalid mode name %q", p.Name, modeName)
		}
		for _, arg := range mode.Args {
			if strings.ContainsRune(arg, '\x00') {
				return fmt.Errorf("profile %q mode %q contains NUL argument", p.Name, modeName)
			}
		}
	}
	if p.DefaultMode != "" {
		if _, ok := p.Modes[p.DefaultMode]; !ok {
			return fmt.Errorf("profile %q default_mode %q does not exist in modes", p.Name, p.DefaultMode)
		}
	}
	return nil
}

func cloneProfile(p AgentProfile) AgentProfile {
	out := p
	out.Modes = make(map[string]AgentMode, len(p.Modes))
	for k, m := range p.Modes {
		mCopy := m
		mCopy.Args = slices.Clone(m.Args)
		out.Modes[k] = mCopy
	}
	return out
}

func (p AgentProfile) resolveMode(requested string) (string, AgentMode, error) {
	modeName := strings.TrimSpace(requested)
	if modeName == "" {
		modeName = p.DefaultMode
	}
	if modeName == "" {
		names := make([]string, 0, len(p.Modes))
		for k := range p.Modes {
			names = append(names, k)
		}
		slices.Sort(names)
		modeName = names[0]
	}
	m, ok := p.Modes[modeName]
	if !ok {
		return "", AgentMode{}, fmt.Errorf("unknown mode %q for agent %q", modeName, p.Name)
	}
	return modeName, m, nil
}
