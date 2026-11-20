package paths

import (
	"strings"
	"testing"
)

// CutPrefix, case ignored or not: no panic, and the rest is a suffix of the
// path as spelled.
func FuzzCutPrefix(f *testing.F) {
	for _, s := range [][2]string{{"/a/b/c", "/a/b"}, {"/A/B", "/a/b"}, {"/a/bc", "/a/b"}, {"/İ/x", "/i̇"}, {"/ß/x", "/SS"}, {"/K/x", "/k"}, {"/", "/"}} {
		f.Add(s[0], s[1], false)
		f.Add(s[0], s[1], true)
	}
	f.Fuzz(func(t *testing.T, p, dir string, nocase bool) {
		old := NoCase
		NoCase = func() bool { return nocase }
		defer func() { NoCase = old }()
		if rest, ok := CutPrefix(p, dir); ok && !strings.HasSuffix(p, rest) {
			t.Fatalf("CutPrefix(%q, %q) = %q, not the end of the path", p, dir, rest)
		}
	})
}
