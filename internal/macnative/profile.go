//go:build darwin

package macnative

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/sylvinus/agent-vm/internal/version"
)

const (
	BundleID            = "io.github.lee-xeo.claude-cage"
	ClientBundleID      = "io.github.lee-xeo.claude-cage-client"
	MacGuestTrialSuffix = ".macos-trial"
	IntelMacTestSuffix  = ".intel-macos-test"
)

// ProfileInfo describes the resolved Cage profile and host app bundle metadata.
type ProfileInfo struct {
	BundleID      string
	BundlePath    string
	ClientOnly    bool
	MacGuestTrial bool
	IntelMacTest  bool
	ConfigPath    string
	ProfileDir    string
	UpdatesDir    string
	Version       string
}

var (
	testCageMu  sync.RWMutex
	testCageBin string
)

// SetTestCageBinary overrides the cage helper executable path for tests.
func SetTestCageBinary(path string) {
	testCageMu.Lock()
	testCageBin = path
	testCageMu.Unlock()
}

// DetectProfile inspects bundle metadata and environment variables to locate the
// active Cage profile directory and configuration file.
func DetectProfile() ProfileInfo {
	home, _ := os.UserHomeDir()
	bundlePath, info := detectAppBundle()
	bundleID, _ := info["CFBundleIdentifier"].(string)
	clientOnly, _ := info["CageClientOnly"].(bool)
	macTrial := bundleID == BundleID+MacGuestTrialSuffix
	intelTest := bundleID == BundleID+IntelMacTestSuffix
	ver, _ := info["CageVersion"].(string)
	if ver == "" {
		ver, _ = info["CFBundleShortVersionString"].(string)
	}
	if ver == "" {
		ver = version.Version
	}

	var configPath string
	switch {
	case macTrial:
		configPath = filepath.Join(home, ".claude-cage", "macos-trial", "config.json")
	case os.Getenv("CLAUDE_CAGE_CONFIG") != "":
		abs, err := filepath.Abs(os.Getenv("CLAUDE_CAGE_CONFIG"))
		if err == nil {
			configPath = abs
		} else {
			configPath = os.Getenv("CLAUDE_CAGE_CONFIG")
		}
	case intelTest:
		configPath = filepath.Join(home, ".claude-cage-intel-test", "config.json")
	case clientOnly:
		configPath = filepath.Join(home, ".claude-cage", "client", "config.json")
	default:
		configPath = filepath.Join(home, ".claude-cage", "config.json")
	}

	profileDir := filepath.Dir(configPath)
	return ProfileInfo{
		BundleID:      bundleID,
		BundlePath:    bundlePath,
		ClientOnly:    clientOnly,
		MacGuestTrial: macTrial,
		IntelMacTest:  intelTest,
		ConfigPath:    configPath,
		ProfileDir:    profileDir,
		UpdatesDir:    filepath.Join(profileDir, "updates"),
		Version:       ver,
	}
}

// PinEnvironment sets CLAUDE_CAGE_CONFIG to the resolved profile path and clears
// unsafe relaxation flags before spawning helper commands or the controller.
func PinEnvironment() ProfileInfo {
	info := DetectProfile()
	_ = os.Setenv("CLAUDE_CAGE_CONFIG", info.ConfigPath)
	for _, key := range []string{
		"AGENT_VM_UNSAFE_OPEN_NETWORK",
		"AGENT_VM_UNSAFE_WRITABLE_GIT",
		"AGENT_VM_UNSAFE_DISABLE_SECURITY_PROMPTS",
	} {
		_ = os.Unsetenv(key)
	}
	return info
}

// CageBinaryPath locates the bundled `cage` (or `cage-client`) helper executable.
func CageBinaryPath() (string, error) {
	testCageMu.RLock()
	override := testCageBin
	testCageMu.RUnlock()
	if override != "" {
		return override, nil
	}
	if env := os.Getenv("CAGE_HELPER_BIN"); env != "" {
		return env, nil
	}
	if resDir, err := mygo.App.Path(mygo.PathResources); err == nil && resDir != "" {
		candidates := []string{
			filepath.Join(resDir, "cage"),
			filepath.Join(resDir, "darwin-arm64", "cage"),
		}
		for _, c := range candidates {
			if st, statErr := os.Stat(c); statErr == nil && !st.IsDir() {
				return c, nil
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates := []string{
			filepath.Clean(filepath.Join(dir, "..", "Resources", "cage")),
			filepath.Join(dir, "cage"),
		}
		for _, c := range candidates {
			if st, statErr := os.Stat(c); statErr == nil && !st.IsDir() {
				return c, nil
			}
		}
	}
	cwd, _ := os.Getwd()
	for _, rel := range []string{
		"resources/darwin-arm64/cage",
		"_output/bin/cage",
		"../../resources/darwin-arm64/cage",
		"../../_output/bin/cage",
	} {
		p := filepath.Join(cwd, rel)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", os.ErrNotExist
}

func detectAppBundle() (string, map[string]any) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err == nil {
		exe = resolved
	}
	// Walk upward to see if running inside *.app/Contents/MacOS/<binary>.
	cur := filepath.Dir(exe)
	for range 4 {
		if strings.HasSuffix(cur, ".app") {
			plistPath := filepath.Join(cur, "Contents", "Info.plist")
			if data, err := os.ReadFile(plistPath); err == nil {
				if parsed, parseErr := ParsePlist(data); parseErr == nil {
					return cur, parsed
				}
			}
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return "", nil
}
