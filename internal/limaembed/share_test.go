package limaembed

import (
	"os"
	"testing"
	"testing/fstest"
)

func TestGuestAgentMatchesGuestAndCurrentBuild(t *testing.T) {
	const darwinFile = "assets/Darwin/lima-guestagent.Darwin-aarch64.gz"
	files := fstest.MapFS{
		"assets/templates/macos.yaml": {Data: []byte("os: Darwin")},
		darwinFile:                    {Data: []byte("darwin")},
	}
	dir := t.TempDir()
	share, err := writeShare(dir, files)
	if err != nil {
		t.Fatal(err)
	}
	path, err := share.GuestAgent("Darwin", "aarch64")
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "darwin" {
		t.Fatalf("Darwin/aarch64: read %q, %v", data, err)
	}
	// Linux guests run as MicroVMs: no Linux guest agent is bundled.
	if path, err := share.GuestAgent("Linux", "aarch64"); err == nil {
		t.Fatalf("used %s for a Linux guest", path)
	}
	if path, err := share.GuestAgent("Darwin", "x86_64"); err == nil {
		t.Fatalf("used %s for an unbundled architecture", path)
	}

	// A different build can reuse the same profile with the old file on disk.
	delete(files, darwinFile)
	share, err = writeShare(dir, files)
	if err != nil {
		t.Fatal(err)
	}
	if path, err := share.GuestAgent("Darwin", "aarch64"); err == nil {
		t.Fatalf("used %s after the build stopped bundling it", path)
	}
}
