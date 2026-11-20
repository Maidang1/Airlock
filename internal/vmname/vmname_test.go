package vmname

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/bashref"
	"github.com/sylvinus/agent-vm/internal/paths"
)

var dirs = []string{
	"/home/u/proj",
	"/home/u/proj/",
	"/home/u/My Project.v2",
	"/home/u/café",
	"/home/u/-lead-",
	"/home/u/__",
	"/home/u/a__b",
	"/home/u/" + strings.Repeat("x", 40),
	"/home/u/a-very_long.name-with.many-parts-2026",
	"/",
	"/home/u/x\ty",
}

func TestName(t *testing.T) {
	cases := map[string]string{
		"/home/u/proj":          "proj-",
		"/home/u/My Project.v2": "My-Project-v2-",
		"/home/u/café":          "caf-",
		"/home/u/-lead-":        "lead-",
		"/":                     "project-",
		"/home/u/__":            "project-",
	}
	for dir, prefix := range cases {
		got := Name(dir)
		if !strings.HasPrefix(got, prefix) || len(got) != len(prefix)+8 {
			t.Errorf("Name(%q) = %q, want %q and 8 hex digits", dir, got, prefix)
		}
	}
	if Name("/a/b") == Name("/a/b/") {
		t.Error("the path is hashed as spelled: /a/b and /a/b/ differ")
	}
}

// The names 0.2 gave, without its prefix: its VMs are found once moved.
func TestNameBash(t *testing.T) {
	for _, dir := range dirs {
		r := bashref.Run(t, "", nil, "_agent_vm_name", dir)
		if r.Code != 0 {
			t.Fatalf("_agent_vm_name %q: %d %s", dir, r.Code, r.Stderr)
		}
		want := strings.TrimSuffix(r.Stdout, "\n")
		if strings.HasPrefix(want, OldPrefix+"-") {
			// Nothing of the folder's name: a name Lima refused in 0.2.
			continue
		}
		if Name(dir) != FromOld(want) {
			t.Errorf("Name(%q) = %q, bash %q", dir, Name(dir), want)
		}
	}
}

// A name longer than this machine allows is cut in its folder part, the
// hash of the whole path kept; 0.2's name of the same folder is cut alike.
func TestNameFits(t *testing.T) {
	limit := Limit(20)
	dir := "/home/u/" + strings.Repeat("long-folder-", 4)
	n := limit.Name(dir)
	full := strings.TrimSuffix(strings.Repeat("long-folder-", 4), "-")
	// The hash is of the whole path, whatever the cut.
	sum := sha256.Sum256([]byte(paths.Guest(dir)))
	hash := "-" + hex.EncodeToString(sum[:])[:8]
	if n != "long-folder"+hash {
		t.Errorf("Name = %q, want %q", n, "long-folder"+hash)
	}
	if other := limit.Name("/elsewhere/" + strings.Repeat("long-folder-", 4)); other == n {
		t.Error("two folders, one name")
	}
	oldName := OldPrefix + Name(dir)
	if limit.FromOld(oldName) != n {
		t.Errorf("FromOld(%q) = %q, Name %q", oldName, limit.FromOld(oldName), n)
	}
	if s, _ := limit.Scratch(dir); len(s) > 20 {
		t.Errorf("Scratch = %q", s)
	}
	if Name(dir) != full+hash {
		t.Errorf("cut though it fits: %q", Name(dir))
	}
}

func TestFromOld(t *testing.T) {
	for old, want := range map[string]string{
		"agent-vm-base":               "base",
		"agent-vm-proj-1234abcd":      "proj-1234abcd",
		"agent-vm-base-1234abcd":      "base-1234abcd",
		"agent-vm-p-scratch-0123abcd": "p-scratch-0123abcd",
	} {
		if got := FromOld(old); got != want {
			t.Errorf("FromOld(%q) = %q, want %q", old, got, want)
		}
	}
}
