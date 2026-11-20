package host

import (
	"path/filepath"
	"testing"
)

// The native macOS capacity API requires an existing URL. First-run storage
// checks must resolve a missing environment directory to its existing parent.
func TestFreeGiBMissingDirectory(t *testing.T) {
	parent := t.TempDir()
	want, ok := FreeGiB(parent)
	if !ok {
		t.Fatal("cannot read existing volume capacity")
	}
	got, ok := FreeGiB(filepath.Join(parent, "not", "created", "lima"))
	if !ok || got < want-1 || got > want+1 {
		t.Fatalf("missing directory capacity = %d (ok=%v), parent = %d", got, ok, want)
	}
}
