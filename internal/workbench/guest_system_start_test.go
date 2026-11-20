package workbench

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sylvinus/agent-vm/internal/guestos"
	"github.com/sylvinus/agent-vm/internal/hostimage"
)

func TestUnsupportedGuestSystemCannotStartDebian(t *testing.T) {
	s := testServer(t)
	for _, system := range []string{"macos", "windows-11", "debian-12", "ubuntu-22.04", "ubuntu-24.04", "unknown"} {
		response := request(s, http.MethodPost, "/api/start", s.origin, `{"guest_system":"`+system+`"}`, true)
		if response.Code != http.StatusConflict || s.jobs.snapshot().ID != "" {
			t.Fatalf("%s: got %d, job %v", system, response.Code, s.jobs.snapshot())
		}
	}
}

func TestIncompleteInstallationPreventsImplicitLinuxStart(t *testing.T) {
	s := testServer(t)
	if err := guestos.SaveInstallation(s.profile, guestos.Installation{Version: 1, System: guestos.MacOS, ID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"{}", `{"guest_system":"debian-13"}`} {
		w := request(s, "POST", "/api/start", s.origin, body, true)
		if w.Code != http.StatusConflict || s.jobs.snapshot().ID != "" {
			t.Fatalf("created a Linux job from pending macOS (%s): %d", body, w.Code)
		}
	}
	if err := s.startEnvironment(false); err == nil {
		t.Fatalf("indirect Linux start: %v", err)
	}
}

// A profile left behind by the removed Lima Linux runtime must be refused
// with one clear message everywhere it could otherwise be revived.
func TestLegacyLimaLinuxProfileIsRefused(t *testing.T) {
	s := testServer(t)
	if err := os.MkdirAll(filepath.Join(s.profile, "state", "lima", "legacy-instance"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := guestos.SaveInstallation(s.profile, guestos.Installation{
		Version: 1,
		ID:      uuid.New(),
		System:  guestos.Debian13,
		Source: hostimage.Plan{
			Host:    hostimage.Identity{System: guestos.Debian13, Arch: "arm64", Version: "13"},
			Version: "13",
			Format:  "qcow2",
			Path:    "/tmp/legacy-linux.qcow2",
			Bytes:   1 << 30,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.attach(); !errors.Is(err, errLimaLinuxRemoved) {
		t.Fatalf("legacy attach: %v", err)
	}
	if err := s.startEnvironment(false); !errors.Is(err, errLimaLinuxRemoved) {
		t.Fatalf("legacy start: %v", err)
	}
	w := request(s, "POST", "/api/start", s.origin, "{}", true)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), errLimaLinuxRemoved.Error()) {
		t.Fatalf("legacy /api/start: %d %s", w.Code, w.Body.String())
	}
	if _, err := s.guestInstallView(); !errors.Is(err, errLimaLinuxRemoved) {
		t.Fatalf("legacy installation record: %v", err)
	}
}

