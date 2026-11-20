package state

import (
	"path/filepath"
	"testing"

	"github.com/sylvinus/agent-vm/internal/paths"
)

func TestDefault(t *testing.T) {
	elsewhere := t.TempDir()
	t.Setenv("AGENT_VM_STATE_DIR", elsewhere)
	if d, err := Default(); err != nil || string(d) != paths.Host(elsewhere) {
		t.Errorf("override: %q, %v", d, err)
	}
	home := t.TempDir()
	t.Setenv("AGENT_VM_STATE_DIR", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if d, err := Default(); err != nil || string(d) != paths.Host(filepath.Join(home, ".agent-vm")) {
		t.Errorf("default: %q, %v", d, err)
	}
}

// A relative folder is made absolute where it is read: the checks that keep
// shares away from the state compare real locations.
func TestDefaultRelative(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	t.Setenv("AGENT_VM_STATE_DIR", "rel/state")
	d, err := Default()
	if err != nil || !filepath.IsAbs(string(d)) || filepath.Base(string(d)) != "state" {
		t.Fatalf("relative: %q, %v", d, err)
	}
	real, _ := filepath.EvalSymlinks(wd)
	got, _ := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(string(d))))
	if got != real {
		t.Errorf("relative to %q, want %q", got, real)
	}
}
