package mounts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/paths"
)

// Where the file system ignores case, the home folder in other capitals is
// the home folder.
func TestUnsafeLocationNoCase(t *testing.T) {
	s := newSandbox(t)
	refs := Refs(s.home, filepath.Join(s.root, "self"), s.state, filepath.Join(s.state, "lima"))
	upper := strings.ToUpper(s.home)
	// On a file system that ignores case the uppercased home is the
	// home folder, and refused below; here case matters.
	if !paths.NoCase() {
		if _, bad := UnsafeLocation(upper, refs); bad {
			t.Error("refused where case matters")
		}
	}
	old := paths.NoCase
	paths.NoCase = func() bool { return true }
	t.Cleanup(func() { paths.NoCase = old })
	refs = Refs(s.home, filepath.Join(s.root, "self"), s.state, filepath.Join(s.state, "lima"))
	if why, bad := UnsafeLocation(upper, refs); !bad || !strings.Contains(why, "your home directory") {
		t.Errorf("home in capitals: %q %v", why, bad)
	}
	if why, bad := UnsafeLocation(strings.ToUpper(s.state)+"/x", refs); !bad {
		t.Errorf("inside the state dir in capitals: %q %v", why, bad)
	}
}

// Folders of credentials and applications' data under the home folder are
// refused as projects, from inside, equal or above, through links, with the
// case a case-ignoring file system gives them; the other folders of the home
// folder are not.
func TestCredentialLocation(t *testing.T) {
	s := newSandbox(t)
	for _, d := range []string{".ssh/keys", ".config/gcloud", "Library/Keychains", ".local/share/keyrings", ".local/src", ".sshx", "work/.ssh-notes", ".mozilla"} {
		os.MkdirAll(filepath.Join(s.home, d), 0o755)
	}
	os.MkdirAll(filepath.Join(s.root, "appdata", "app"), 0o755)
	t.Setenv("APPDATA", filepath.Join(s.root, "appdata"))
	t.Setenv("LOCALAPPDATA", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	os.Symlink(s.home+"/.ssh", s.proj+"/lnk")
	refs := s.refs()
	for _, c := range []struct {
		dir string
		bad bool
	}{
		{s.home + "/.ssh", true},
		{s.home + "/.ssh/keys", true},
		{s.home + "/.config", true},
		{s.home + "/.config/gcloud", true},
		{s.home + "/Library/Keychains", true},
		{s.home + "/.mozilla", true},
		{s.home + "/.local", true},
		{s.home + "/.local/share", true},
		{s.home + "/.local/share/keyrings", true},
		{s.proj + "/lnk", true},
		{s.root + "/appdata/app", true},
		{s.proj, false},
		{s.proj + "/sub", false},
		{s.home + "/.local/src", false},
		{s.home + "/.sshx", false},
		{s.home + "/work/.ssh-notes", false},
		{s.root + "/data/a", false},
	} {
		why, bad := CredentialLocation(c.dir, refs)
		if bad != c.bad {
			t.Errorf("%s: %q %v", c.dir, why, bad)
		}
		if _, bad := UnsafeProject(c.dir, refs); c.bad && !bad {
			t.Errorf("UnsafeProject lets %s through", c.dir)
		}
	}
	if why, _ := CredentialLocation(s.home+"/.ssh", refs); !strings.Contains(why, "~/.ssh") {
		t.Errorf("why: %q", why)
	}
	// A credentials folder that is a link to another: that one counts too.
	os.RemoveAll(s.home + "/.aws")
	os.MkdirAll(s.root+"/dotfiles/aws", 0o755)
	os.Symlink(s.root+"/dotfiles/aws", s.home+"/.aws")
	if _, bad := CredentialLocation(s.root+"/dotfiles/aws", refs); !bad {
		t.Error("the target of ~/.aws was let through")
	}
	// Other capitals, where the file system ignores case.
	old := paths.NoCase
	paths.NoCase = func() bool { return true }
	t.Cleanup(func() { paths.NoCase = old })
	refs = s.refs()
	if _, bad := CredentialLocation(strings.ToUpper(s.home)+"/.SSH", refs); !bad {
		t.Error("~/.SSH let through where case is ignored")
	}
}
