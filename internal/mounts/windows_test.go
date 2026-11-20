package mounts

import (
	"os"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/paths"
)

// A repository's own folder is found up to the drive, the drive's root
// excepted, as "/" is elsewhere.
func TestWindowsGitDirShare(t *testing.T) {
	old := paths.Windows
	paths.Windows = func() bool { return true }
	t.Cleanup(func() { paths.Windows = old })
	t.Chdir(t.TempDir())
	for _, d := range []string{"C:/objects", "C:/refs", "C:/proj", "C:/bare/objects", "C:/bare/refs", "C:/bare/sub"} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile("C:/HEAD", nil, 0o644)
	os.WriteFile("C:/bare/HEAD", nil, 0o644)
	if why, bad := GitDirShare("C:/proj"); bad {
		t.Errorf("the root taken for a repository: %s", why)
	}
	if why, bad := GitDirShare("C:/bare/sub"); !bad || !strings.Contains(why, "(C:/bare)") {
		t.Errorf("inside a bare repository: %q %v", why, bad)
	}
}
