package guestos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sylvinus/agent-vm/internal/hostimage"
)

func TestHostSourcePublishesOneVerifiedMediaReceipt(t *testing.T) {
	profile := t.TempDir()
	source := hostimage.Plan{Host: hostimage.Identity{System: Windows11, Version: "25H2", Build: "26200.9", Edition: "Professional", Language: "zh-CN", Arch: "amd64"}, Version: "25H2", Build: "26200.8", Format: "esd", URL: "http://dl.delivery.mp.microsoft.com/example.esd", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 10}
	install := Installation{Version: 1, ID: uuid.New(), System: Windows11, EditorTerms: true, Source: source}
	if err := SaveInstallation(profile, install); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(filepath.Join(profile, installRecord))
	ready := source
	ready.Path = filepath.Join(profile, "system.esd")
	ready.Modified = 123
	if err := RecordPreparedMedia(profile, install.ID, ready); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadInstallation(profile)
	if err != nil || loaded.Media != ready.Path || loaded.Source != ready {
		t.Fatalf("loaded: %+v %v", loaded, err)
	}
	if err := SaveInstallation(profile, *loaded); err != nil {
		t.Fatal(err)
	}
	if err := RecordPreparedMedia(profile, install.ID, ready); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(profile, installRecord))
	if string(original) != string(after) {
		t.Fatal("changed original installation choice")
	}
	changed := ready
	changed.Host.Edition = "Enterprise"
	if err := RecordPreparedMedia(profile, install.ID, changed); err == nil {
		t.Fatal("accepted another edition")
	}
	if err := RecordPreparedMedia(profile, uuid.New(), ready); err == nil {
		t.Fatal("accepted another installation")
	}
	changed = ready
	changed.Path = filepath.Join(profile, "other.esd")
	if err := RecordPreparedMedia(profile, install.ID, changed); err == nil {
		t.Fatal("replaced published media")
	}
}

func TestLegacyInstallationOmitsSource(t *testing.T) {
	profile := t.TempDir()
	if err := SaveInstallation(profile, Installation{Version: 1, ID: uuid.New(), System: MacOS}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(profile, installRecord))
	if strings.Contains(string(data), `"source"`) {
		t.Fatal("legacy choice acquired source metadata")
	}
}

func TestWindowsCloneCleanupTracksRetainedCredential(t *testing.T) {
	profile := t.TempDir()
	source := hostimage.Plan{
		Host:    hostimage.Identity{System: Windows11, Version: "25H2", Edition: "Professional", Language: "zh-CN", Arch: "amd64"},
		Version: "25H2", Build: "26200.8", Format: "esd", Path: filepath.Join(profile, "system.esd"),
	}
	install := Installation{Version: 1, ID: uuid.New(), System: Windows11, EditorTerms: true, Source: source}
	if err := SaveInstallation(profile, install); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(profile, "windows-setup-"+strings.ReplaceAll(install.ID.String(), "-", "")+".dpapi")
	if err := os.WriteFile(path, []byte("encrypted-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	pending, err := WindowsCloneCleanupPending(profile)
	if err != nil || !pending {
		t.Fatalf("retained credential bypassed cleanup: %v %v", pending, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	pending, err = WindowsCloneCleanupPending(profile)
	if err != nil || pending {
		t.Fatalf("completed cleanup still blocks activation: %v %v", pending, err)
	}
}
