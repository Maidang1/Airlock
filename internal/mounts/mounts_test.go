package mounts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sylvinus/agent-vm/internal/bashref"
	"github.com/sylvinus/agent-vm/internal/paths"
)

// sandbox: a home with a project in it, and folders around.
type sandbox struct {
	root, home, proj, state string
}

func newSandbox(t *testing.T) sandbox {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	// Host-spelled, as the callers pass their folders: mixed separators
	// never compare on Windows.
	root = paths.Host(root)
	s := sandbox{root: root, home: root + "/home", proj: root + "/home/proj"}
	s.state = s.home + "/.agent-vm"
	for _, d := range []string{s.proj + "/sub", s.state, root + "/data/a", root + "/data/b", root + "/bare.git/objects", root + "/bare.git/refs", root + "/repo/.git/hooks", s.home + "/.lima"} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(root+"/bare.git/HEAD", []byte("ref: refs/heads/main\n"), 0o644)
	os.WriteFile(root+"/data/file.txt", []byte("x"), 0o644)
	os.Symlink(root+"/data/a", root+"/alink")
	return s
}

func (s sandbox) refs() []Ref {
	return Refs(s.home, bashref.Root(), s.state, s.home+"/.lima")
}

func (s sandbox) bash(t *testing.T, snippet string, args ...string) bashref.Result {
	return bashref.Script(t, "", []string{"HOME=" + s.home, "AGENT_VM_STATE_DIR=" + s.state}, snippet, args...)
}

func TestUnsafeProjectBash(t *testing.T) {
	s := newSandbox(t)
	for _, d := range []string{s.proj, s.home, s.root, s.state, s.state + "/x", bashref.Root(), bashref.Root() + "/lib", s.home + "/.lima",
		s.root + "/bare.git", s.root + "/bare.git/objects", s.root + "/repo/.git", s.root + "/repo/.GIT", s.root + "/repo/.git/hooks", s.root + "/repo", s.root + "/alink"} {
		os.MkdirAll(d, 0o755)
		why, bad := UnsafeProject(d, s.refs())
		r := s.bash(t, `_agent_vm_unsafe_project "$1"`, d)
		if bad != (r.Code == 0) || (bad && why+"\n" != r.Stdout) {
			t.Errorf("UnsafeProject(%q) = %q %v, bash %q %d", d, why, bad, r.Stdout, r.Code)
		}
	}
}
