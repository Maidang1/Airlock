//go:build cageclient

package workbench

import (
	"errors"
	"fmt"

	"github.com/sylvinus/agent-vm/internal/cage"
)

type macOSResourceRequirements struct {
	Version   int    `json:"version"`
	MemoryGiB int    `json:"minimum_memory_gib"`
	CPUs      int    `json:"minimum_cpus"`
	Source    string `json:"source"`
}

func (requirements macOSResourceRequirements) Limits(allocation cage.ResourceAllocation) (int, int, error) {
	memory, cpus, err := allocation.Limits()
	if err != nil {
		return 0, 0, err
	}
	if allocation.Automatic && memory < requirements.MemoryGiB {
		memory, _ = cage.ResourceLimits()
		memory = min(memory, requirements.MemoryGiB)
	}
	if memory < requirements.MemoryGiB || cpus < requirements.CPUs {
		return 0, 0, fmt.Errorf("这台 macOS 客机至少需要 %d GiB 内存与 %d 个 CPU 逻辑核心，请调整资源配置", requirements.MemoryGiB, requirements.CPUs)
	}
	return memory, cpus, nil
}

func readMacOSResourceRequirements(string) (macOSResourceRequirements, error) {
	return macOSResourceRequirements{}, errors.New("轻量远程客户端不包含本机虚拟机运行时")
}
