//go:build !cageclient

package workbench

import (
	"github.com/sylvinus/agent-vm/internal/limaembed"
	"github.com/sylvinus/agent-vm/internal/vm"
)

type macOSResourceRequirements = vm.MacOSResourceRequirements

func readMacOSResourceRequirements(instanceDir string) (macOSResourceRequirements, error) {
	return vm.ReadMacOSResourceRequirements(instanceDir)
}

func checkVirGLHost() error {
	return vm.CheckVirGLHost()
}

func readLimaImageTemplate(system string) ([]byte, error) {
	return limaembed.ReadImageTemplate(system)
}
