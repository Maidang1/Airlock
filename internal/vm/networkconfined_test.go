package vm

import (
	"errors"
	"testing"

	"github.com/lima-vm/lima/v2/pkg/limatype"
)

func TestNetworkConfined(t *testing.T) {
	yes := true
	for name, tc := range map[string]struct {
		networks []limatype.Network
		open     bool
	}{
		"none":         {nil, false},
		"shared":       {[]limatype.Network{{Lima: "user-v2"}}, true},
		"socket":       {[]limatype.Network{{Socket: "/tmp/vmnet.sock"}}, true},
		"vzNAT":        {[]limatype.Network{{VZNAT: &yes}}, true},
		"interface":    {[]limatype.Network{{Interface: "lima0"}}, true},
		"second entry": {[]limatype.Network{{Lima: "a"}, {Lima: "b"}}, true},
	} {
		err := NetworkConfined(&limatype.LimaYAML{Networks: tc.networks})
		if tc.open != errors.Is(err, ErrNetworkOpen) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNetworkConfinedRejectsDriversThatBypassNetstack(t *testing.T) {
	for _, driver := range []limatype.VMType{limatype.WSL2, limatype.HCS, "external"} {
		if err := NetworkConfined(&limatype.LimaYAML{VMType: &driver}); !errors.Is(err, ErrNetworkOpen) {
			t.Fatalf("driver %q without explicit networks bypassed the egress policy: %v", driver, err)
		}
	}
	for _, driver := range []limatype.VMType{limatype.QEMU, limatype.VZ} {
		if err := NetworkConfined(&limatype.LimaYAML{VMType: &driver}); err != nil {
			t.Fatalf("confined driver %q rejected: %v", driver, err)
		}
	}
}
