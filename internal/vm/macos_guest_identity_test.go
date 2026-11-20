//go:build darwin && arm64 && cgo && !no_vz

package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lima-vm/lima/v2/pkg/limayaml"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestprofile"
	"github.com/sylvinus/agent-vm/internal/limaembed"
)

// newIdentityLima is this file's own harness; the wrapper tests keep theirs.
// It skips when the Lima share was not built (make assets).
func newIdentityLima(t *testing.T) *Lima {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	t.Setenv("LIMA_TEMPLATES_PATH", "")
	l, err := NewLima(home, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.prepare(); errors.Is(err, limaembed.ErrNoAssets) {
		t.Skip("built without the guest agent: make assets")
	} else if err != nil {
		t.Fatal(err)
	}
	return l
}

// writeFakeIPSW pins a placeholder restore image the way the installer pins
// the selected one; creation only records the location and digest.
func writeFakeIPSW(t *testing.T) (path, pin string) {
	t.Helper()
	content := []byte("not a real restore image, only pinned")
	path = filepath.Join(t.TempDir(), "pinned.ipsw")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return path, "sha256:" + hex.EncodeToString(sum[:])
}

func writeMacOSInstallTemplate(t *testing.T, ipsw, pin string) string {
	t.Helper()
	body := fmt.Sprintf(macOSInstallTemplate, strconv.Quote(ipsw), pin)
	path := filepath.Join(t.TempDir(), "macos.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Local Linux environments run as MicroVMs now: Lima's stock Linux templates
// are still embedded, so creating one must be refused with where Linux went,
// and nothing may be left behind.
func TestLimaRefusesLinuxTemplates(t *testing.T) {
	l := newIdentityLima(t)
	for _, template := range []string{"template:debian-13", "template:ubuntu-22.04"} {
		err := l.Create(t.Context(), "refused", template, Settings{}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "Lima creates only macOS guests; local Linux environments run as MicroVMs") {
			t.Fatalf("%s under Lima: %v", template, err)
		}
	}
	if _, err := l.Get(t.Context(), "refused"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused creation left an instance behind: %v", err)
	}
}

// Creation applies the configured hostname as the guest identity, fixes the
// guest account, and records exactly the selected restore image with no host
// mounts; recreation of the same management name gets fresh hardware
// fingerprints, and creation without a cage profile generates its own.
func TestMacOSConfiguredIdentity(t *testing.T) {
	l := newIdentityLima(t)
	profileDir := t.TempDir()
	configPath := filepath.Join(profileDir, "config.json")
	data, err := json.Marshal(cage.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := guestprofile.Create(profileDir, guestprofile.Options{Hostname: "MY-PC", Username: "alex", BrowserUsername: "web"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(cage.ConfigEnv, configPath)
	ipsw, pin := writeFakeIPSW(t)
	template := writeMacOSInstallTemplate(t, ipsw, pin)
	ctx := t.Context()
	if err := l.Create(ctx, "mac", template, Settings{NoContainerd: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	inst, err := l.inspect(ctx, "mac")
	if err != nil {
		t.Fatal(err)
	}
	cfg := inst.Config
	identity := cfg.GuestIdentity
	if identity == nil || identity.Hostname != p.Hostname {
		t.Fatalf("creation did not apply the configured hostname: %+v", identity)
	}
	if err := identity.Validate(); err != nil {
		t.Fatalf("configured identity invalid: %v", err)
	}
	if limayaml.GuestHostname(cfg, "mac") != p.Hostname {
		t.Fatal("guest configuration ignored the saved identity")
	}
	if limayaml.GuestMACAddress(cfg, inst.Dir) != identity.MACAddress {
		t.Fatal("guest configuration ignored the saved MAC address")
	}
	if cfg.User.Name == nil || *cfg.User.Name != "cageprovision" || cfg.User.Home == nil || *cfg.User.Home != "/Users/cageprovision" {
		t.Fatalf("creation did not fix the guest account: %+v", cfg.User)
	}
	if cfg.User.UID == nil || *cfg.User.UID != 501 {
		t.Fatalf("guest account uid changed: %+v", cfg.User.UID)
	}
	if len(cfg.Images) != 1 || cfg.Images[0].Location != ipsw || string(cfg.Images[0].Digest) != pin {
		t.Fatalf("unexpected image selection: %+v", cfg.Images)
	}
	if len(cfg.Mounts) != 0 {
		t.Fatal("host directories were mounted")
	}
	if cfg.TimeZone == nil || *cfg.TimeZone != cage.DefaultConfig().Timezone {
		t.Fatalf("creation did not apply the configured timezone: %v", cfg.TimeZone)
	}
	// Recreating the same management name must not reuse its fingerprints.
	first := *identity
	if err := l.Delete(ctx, "mac"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := l.Create(ctx, "mac", template, Settings{NoContainerd: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	recreated, err := l.inspect(ctx, "mac")
	if err != nil {
		t.Fatal(err)
	}
	again := recreated.Config.GuestIdentity
	if again == nil || again.Hostname != p.Hostname || again.UUID == first.UUID {
		t.Fatalf("recreation reused a machine identity: %+v vs %+v", first, again)
	}
	// Without a cage profile, creation generates an identity of its own.
	t.Setenv(cage.ConfigEnv, "")
	if err := l.Create(ctx, "bare", template, Settings{NoContainerd: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	bare, err := l.inspect(ctx, "bare")
	if err != nil {
		t.Fatal(err)
	}
	generated := bare.Config.GuestIdentity
	if generated == nil || generated.Hostname == "" || generated.Hostname == p.Hostname {
		t.Fatalf("bare creation did not generate an identity: %+v", generated)
	}
	if err := generated.Validate(); err != nil {
		t.Fatalf("generated identity invalid: %v", err)
	}
}

// A resource edit keeps the saved identity, as it did before the Linux
// removal: exprs never carries identity or account terms, and Edit of a
// stopped VM applies only what it was given.
func TestMacOSEditPreservesGuestIdentity(t *testing.T) {
	l := newIdentityLima(t)
	ipsw, pin := writeFakeIPSW(t)
	template := writeMacOSInstallTemplate(t, ipsw, pin)
	ctx := t.Context()
	if err := l.Create(ctx, "mac", template, Settings{NoContainerd: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	before, err := l.inspect(ctx, "mac")
	if err != nil {
		t.Fatal(err)
	}
	saved := *before.Config.GuestIdentity
	if err := l.Edit(ctx, "mac", Settings{CPUs: 4, MemoryGiB: 8}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	after, err := l.inspect(ctx, "mac")
	if err != nil {
		t.Fatal(err)
	}
	if after.Config.GuestIdentity == nil || !reflect.DeepEqual(*after.Config.GuestIdentity, saved) {
		t.Fatalf("edit changed guest identity: %+v -> %+v", saved, after.Config.GuestIdentity)
	}
	if after.Config.CPUs == nil || *after.Config.CPUs != 4 {
		t.Fatalf("edit did not apply the new resources: %+v", after.Config.CPUs)
	}
}

func TestEditExpressionsNeverTouchIdentityOrAccount(t *testing.T) {
	port := 2222
	e, err := exprs(Settings{CPUs: 4, MemoryGiB: 8, DiskGiB: 64, NoContainerd: true, SSHLocalPort: &port, macOSTimezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	on := true
	if installer, err := exprs(Settings{macOSInstaller: &on}); err != nil {
		t.Fatal(err)
	} else {
		e = append(e, installer...)
	}
	for _, x := range e {
		if strings.Contains(x, ".guestIdentity") || strings.Contains(x, ".user.") {
			t.Fatalf("edit expression rewrites identity or account: %s", x)
		}
	}
}
