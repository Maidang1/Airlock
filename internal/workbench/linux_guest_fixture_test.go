package workbench

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/containerenv"
)

// Tests stand in for a running Linux guest with a running record under
// state/lima, as their fixtures write it. Production reads no such record:
// the Lima Linux runtime is gone and only containers run outside MicroVM.
func init() {
	containerAlive := vmAlive
	vmAlive = func(profile string) bool {
		if containerenv.Configured(profile) {
			return containerAlive(profile)
		}
		workspace := environmentWorkspace(profile)
		if workspace == "" {
			return false
		}
		data, err := os.ReadFile(filepath.Join(profile, "state", "lima", cage.InstanceName(profile, workspace), "cage-running.json"))
		if err != nil {
			return false
		}
		var record struct {
			PID     int    `json:"pid"`
			Started string `json:"started"`
		}
		if json.Unmarshal(data, &record) != nil || record.PID < 1 {
			return false
		}
		return processExists(record.PID) && cage.ProcessStartMatches(record.PID, record.Started)
	}
}
