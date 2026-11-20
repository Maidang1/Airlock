// Package state is the VM state folder (AGENT_VM_STATE_DIR, else
// ~/.agent-vm), where the hostagent reads its network policy.
package state

import (
	"os"
	"path/filepath"

	"github.com/sylvinus/agent-vm/internal/paths"
)

// Dir is the state folder.
type Dir string

// Default is $AGENT_VM_STATE_DIR, made absolute so that the checks that keep
// shares away from it compare real locations, or ~/.agent-vm, spelled as
// paths.Host does.
func Default() (Dir, error) {
	if d := os.Getenv("AGENT_VM_STATE_DIR"); d != "" {
		abs, err := filepath.Abs(d)
		if err != nil {
			return "", err
		}
		return Dir(paths.Host(abs)), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return Dir(paths.Host(filepath.Join(home, ".agent-vm"))), nil
}
