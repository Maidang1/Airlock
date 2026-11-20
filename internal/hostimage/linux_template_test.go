package hostimage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ClearLinuxTemplate is the cleanup path for choices made by the removed Lima
// Linux runtime: it releases exactly the template it once pinned, together
// with its source record, and refuses to touch a directory whose ownership
// changed in the meantime.
func TestClearLinuxTemplateReleasesOnlyItsOwnPinning(t *testing.T) {
	plan := Plan{
		Host:    Identity{System: "debian-13", Arch: "arm64", Version: "13"},
		Version: "13",
		Format:  "qcow2",
		Digest:  "sha256:" + strings.Repeat("0", 64),
		Path:    "/tmp/legacy.qcow2",
		Bytes:   1 << 30,
	}
	expected, err := linuxTemplateData(plan)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	dir := filepath.Join(state, "guest-media")

	// A profile without a pinned template is not an error.
	if err := ClearLinuxTemplate(state, plan); err != nil {
		t.Fatalf("missing template: %v", err)
	}

	// The pinned template and its source record are released together.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host-system.yaml"), expected, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sourceRecord), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ClearLinuxTemplate(state, plan); err != nil {
		t.Fatalf("release: %v", err)
	}
	for _, name := range []string{"host-system.yaml", sourceRecord} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived the release: %v", name, err)
		}
	}

	// A template pinned by someone else stays untouched.
	foreign := strings.Replace(string(expected), plan.Path, "/tmp/someone-else.qcow2", 1)
	if err := os.WriteFile(filepath.Join(dir, "host-system.yaml"), []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ClearLinuxTemplate(state, plan); err == nil || err.Error() != "本机系统模板归属已变化" {
		t.Fatalf("foreign template: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "host-system.yaml")); err != nil {
		t.Fatalf("foreign template was deleted: %v", err)
	}
}
