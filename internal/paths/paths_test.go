package paths

import (
	"testing"
)

// simulateWindows makes the host a Windows one for the test.
func simulateWindows(t *testing.T, on bool) {
	old := Windows
	Windows = func() bool { return on }
	t.Cleanup(func() { Windows = old })
}

// On Windows, a drive is the root: ".." stops there, and C:/x is absolute.
func TestWindowsPaths(t *testing.T) {
	simulateWindows(t, true)
	for p, want := range map[string]bool{"C:/x": true, "c:/": true, "/x": true, "C:x": false, "x": false, "1:/x": false} {
		if IsAbs(p) != want {
			t.Errorf("IsAbs(%q) != %v", p, want)
		}
	}
	for p, want := range map[string]string{`C:\Users\me`: "/c/Users/me", "D:/x/y": "/d/x/y", "C:/": "/c/", "/tmp": "/tmp"} {
		if got := Guest(p); got != want {
			t.Errorf("Guest(%q) = %q, want %q", p, got, want)
		}
	}
	if Host(`C:\Users\me\proj`) != "C:/Users/me/proj" || Root("C:/Users") != "C:/" || Root("/x") != "/" {
		t.Error("Host or Root")
	}
	// Elsewhere, none of it.
	simulateWindows(t, false)
	if Guest("C:/x") != "C:/x" || IsAbs("C:/x") || Host(`a\b`) != `a\b` {
		t.Error("not Windows")
	}
}
