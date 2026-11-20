package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/version"
)

func TestEntryDropsInheritedUnsafeSwitchesBeforeDispatch(t *testing.T) {
	// A benign early-return command exercises the entry boundary without a VM,
	// Git configuration changes, or access to the user's Cage profile.
	unsafe := []string{
		"AGENT_VM_UNSAFE_OPEN_NETWORK",
		"AGENT_VM_UNSAFE_WRITABLE_GIT",
		"AGENT_VM_UNSAFE_DISABLE_SECURITY_PROMPTS",
	}
	for _, name := range unsafe {
		t.Setenv(name, "1")
	}
	t.Setenv("CLAUDE_CAGE_CONFIG", "/not-accessed-by-help/config.json")
	previous := os.Args
	os.Args = []string{"cage", "help"}
	t.Cleanup(func() { os.Args = previous })
	if code := run(); code != 0 {
		t.Fatalf("help returned %d", code)
	}
	for _, name := range unsafe {
		if _, exists := os.LookupEnv(name); exists {
			t.Errorf("%s survived the Cage entry point", name)
		}
	}
	if got := os.Getenv("CLAUDE_CAGE_CONFIG"); got != "/not-accessed-by-help/config.json" {
		t.Errorf("profile selection changed: %q", got)
	}
}

// TestStatelessCommandsNeedNoProfile covers what an installer or a packaging
// script runs before a profile exists: these commands must answer without
// reading, creating or logging into the Cage profile.
func TestStatelessCommandsNeedNoProfile(t *testing.T) {
	if version.Version == "" {
		t.Fatal("this test build has no version")
	}
	cases := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "help", args: []string{"help"}, wantStdout: "Claude Cage"},
		{name: "short help", args: []string{"-h"}, wantStdout: "Claude Cage"},
		{name: "version", args: []string{"version"}, wantStdout: version.Version},
		{name: "long version", args: []string{"--version"}, wantStdout: version.Version},
		{name: "short version", args: []string{"-V"}, wantStdout: version.Version},
		{name: "version gate met", args: []string{"version", "--min", "0.0.0"}},
		{name: "version gate missed", args: []string{"version", "--min", "99.0.0"}, wantCode: 1, wantStderr: "Update it:  update Cage"},
		{name: "licenses", args: []string{"licenses"}, wantStdout: "MIT License"},
		{name: "licenses usage", args: []string{"licenses", "x"}, wantCode: 2, wantStderr: "Usage: claude-cage licenses"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profileDir := filepath.Join(t.TempDir(), "profile")
			home := t.TempDir()
			t.Setenv("CLAUDE_CAGE_CONFIG", filepath.Join(profileDir, "config.json"))
			t.Setenv("HOME", home)

			previous := os.Args
			os.Args = append([]string{"cage"}, tc.args...)
			t.Cleanup(func() { os.Args = previous })

			code, stdout, stderr := captureRun(t, run)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d (stdout %q, stderr %q)", code, tc.wantCode, stdout, stderr)
			}
			if tc.wantStdout != "" && !strings.Contains(stdout, tc.wantStdout) {
				t.Errorf("stdout %q does not contain %q", stdout, tc.wantStdout)
			}
			if tc.wantStderr != "" && !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr %q does not contain %q", stderr, tc.wantStderr)
			}
			// The license texts name agent-vm as an upstream; messages must not.
			if strings.Contains(stderr, "agent-vm") {
				t.Errorf("a Cage message names agent-vm: %q", stderr)
			}
			if _, err := os.Stat(profileDir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("profile directory was created: %v", err)
			}
			if _, err := os.Stat(filepath.Join(home, ".claude-cage")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("home profile was created")
			}
		})
	}
}

// captureRun redirects the process streams for the entry point, which writes
// through os.Stdout and os.Stderr.
func captureRun(t *testing.T, fn func() int) (int, string, string) {
	t.Helper()
	directory := t.TempDir()
	stdout, err := os.Create(filepath.Join(directory, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(directory, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	previousStdout, previousStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	defer func() { os.Stdout, os.Stderr = previousStdout, previousStderr }()

	code := fn()
	for _, file := range []*os.File{stdout, stderr} {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	out, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	errOut, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(out), string(errOut)
}
func TestContainerInstallsGitHubCLIByDefault(t *testing.T) {
	script, err := containerInstallScript("tools")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "export AGENT_VM_INSTALL_GH=1\n") {
		t.Fatal("fresh cloud environments would omit GitHub CLI")
	}
}
