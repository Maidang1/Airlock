//go:build !cageclient

package workbench

import "github.com/sylvinus/agent-vm/internal/vm"

type macOSResourceRequirements = vm.MacOSResourceRequirements

func readMacOSResourceRequirements(instanceDir string) (macOSResourceRequirements, error) {
	return vm.ReadMacOSResourceRequirements(instanceDir)
}
