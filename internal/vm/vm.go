// Package vm runs the virtual Mac guests with the Lima built into Cage
// (lima.go). Local Linux guests run as MicroVMs instead (internal/microvm).
package vm

import (
	"errors"
	"io"
)

// ErrNotFound is returned for a VM that does not exist.
var ErrNotFound = errors.New("no such VM")

// ErrLimaOverrides is returned while the backend's own config would add to
// what agent-vm sets for every VM (Lima's _config/default.yaml and
// override.yaml).
var ErrLimaOverrides = errors.New("agent-vm does not use Lima's default.yaml or override.yaml; move it out of the way")

// Status of a VM.
type Status string

const (
	Running Status = "Running"
	Stopped Status = "Stopped"
	// Broken: the backend cannot tell, or the VM is half there.
	Broken Status = "Broken"
)

// Instance is a VM as the backend has it.
type Instance struct {
	Name         string
	Status       Status
	Dir          string // the backend's folder for it
	CPUs         int
	Memory, Disk int64 // bytes
	SSHLocalPort int   // the port in use
	// SSHPortConfig is the port the config fixes, 0 when the backend
	// picks one on each start.
	SSHPortConfig int
	SSHAddress    string
	// SSHConfig is the ssh_config file the backend rewrites on each start,
	// for tools that connect to the VM.
	SSHConfig    string
	VMType, Arch string
	// ConfigErr is why the backend cannot read the VM's config (Broken).
	ConfigErr error
	// Orphaned: Broken, the VM still running without the backend's process
	// that serves it (Lima's hostagent). Nothing reaches it; Stop ends it by
	// force.
	Orphaned bool
}

// Settings change a VM. A zero field is left as it is.
type Settings struct {
	CPUs         int
	MemoryGiB    int
	DiskGiB      int
	SSHLocalPort *int // 0 gives the port back to the backend
	// NoContainerd leaves out the backend's own containerd.
	NoContainerd bool

	// noEphemeralPorts: see ephemeralPorts.
	noEphemeralPorts bool
	// Refresh the cloud-init timezone on cold starts of an existing virtual Mac.
	macOSTimezone  string
	macOSInstaller *bool
}

// ephemeralPorts is the guest kernel's range for the ports a program gets
// when it asks for any free one (Debian's ip_local_port_range). Never
// forwarded: nobody chose them (Chromium's DevTools port, language
// servers...), and each would land on the host's loopback with a
// notification.
var ephemeralPorts = [2]int{32768, 60999}

// ShellOpts is a command run in a running VM.
type ShellOpts struct {
	// Workdir is where the command runs; it fails when it cannot cd there.
	Workdir string
	// Args is the command, run by the user's login shell; empty for the
	// shell itself.
	Args           []string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// Interactive asks for a terminal in the guest, as a full-screen
	// program needs, when stdin is one. Stdout on a terminal gets one
	// anyway.
	Interactive bool
}
