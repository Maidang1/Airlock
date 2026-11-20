package cage

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/guestprofile"
)

func TestConfiguredGuestScripts(t *testing.T) {
	p, e := guestprofile.New(guestprofile.Options{Username: "alex", BrowserUsername: "web", AudioName: "speaker123", AudioDescription: "USB Speakers", ExtensionName: "My Tab Layout", DisplayWidth: 1920, DisplayHeight: 1080, BrowserWidth: 1280, BrowserHeight: 720, WebGL: "disabled", Canvas: "blocked"})
	if e != nil {
		t.Fatal(e)
	}
	c := DefaultConfig()
	c.GuestProfile = p
	for name, script := range map[string]string{"start": c.StartScript()} {
		for _, bad := range []string{"/home/cage", "User=cage\n", "User=cage-browser\n", "-u cage-browser ", "-u cage ", "cage-browser ALL=", "getpwnam('cage')", "getpwnam('cage-browser')", "cage_output", "2880x1800", "--window-size=1440,900", "Cage browser panes"} {
			if strings.Contains(script, bad) {
				t.Fatalf("%s retains %q", name, bad)
			}
		}
		for _, want := range []string{`"-u", "alex", "/usr/local/bin/cage-native-input"`, `"-u", "alex", executable`, "sudo -H -u web xdg-mime", "User=web\n", "/home/alex", "--window-size=1280,720", "--disable-reading-from-canvas", "--disable-webgl", "browser-panes-" + p.ExtensionSuffix, `"name": "My Tab Layout"`, "set-default-source speaker123.monitor", "1920x1080"} {
			if !strings.Contains(script, want) {
				t.Fatalf("%s misses %q", name, want)
			}
		}
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(script)
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("%s invalid: %v %s", name, e, b)
		}
	}
}

func TestCustomHomesInStartScript(t *testing.T) {
	p, err := guestprofile.New(guestprofile.Options{Username: "alex", BrowserUsername: "web", WorkHome: "/home/projects", BrowserHome: "/home/browsing"})
	if err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.GuestProfile = p
	script := c.StartScript()
	for _, want := range []string{"/home/projects", "/home/browsing"} {
		if !strings.Contains(script, want) {
			t.Fatalf("start script misses the configured home %q", want)
		}
	}
	if p.Linux(script) != script {
		t.Fatal("start script changes on repeated profile rendering")
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start script invalid shell: %v %s", err, data)
	}
}
